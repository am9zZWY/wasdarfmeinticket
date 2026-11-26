// Package check decides whether a ticket covers the legs of a journey.
//
// The guiding rule: "valid" is only answered when a rule or the fare
// source positively supports it. Missing data yields "unknown".
package check

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

// Ticket types.
const (
	TypeDeutschlandticket = "deutschlandticket"
	TypeLaenderticket     = "laenderticket"
	TypeVerbund           = "verbund"
)

// Ticket is a ticket the user holds.
type Ticket struct {
	Type string `json:"type"`
	// Name selects the product for type "laenderticket".
	Name string `json:"name,omitempty"`
	// Net is the Verbund for type "verbund", e.g. "naldo", "vhb", "mvv", "vgn".
	Net string `json:"net,omitempty"`
	// Zones are the zones the ticket is valid for (type "verbund").
	Zones []string `json:"zones,omitempty"`
	// Network marks a ticket valid in the whole Verbund (type "verbund").
	// It is implied when neither zones nor a route are given.
	Network bool `json:"network,omitempty"`
	// From/To or Link describe a "verbund" ticket by the route it was bought
	// for instead of by zones: two stop names, or a DB share link of that
	// route. Net and Zones are then looked up, see NeedsZoneLookup.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Link string `json:"link,omitempty"`
}

// Normalized makes implicit meanings explicit: a "verbund" ticket that names
// only its net, without zones or a route, is a ticket for the whole Verbund.
func (ticket Ticket) Normalized() Ticket {
	if ticket.Type == TypeVerbund && len(ticket.Zones) == 0 && !ticket.NeedsZoneLookup() {
		ticket.Network = true
	}
	return ticket
}

// NeedsZoneLookup reports whether the zones of a "verbund" ticket still have
// to be derived from its route.
func (ticket Ticket) NeedsZoneLookup() bool {
	return ticket.Type == TypeVerbund && !ticket.Network && len(ticket.Zones) == 0 && (ticket.Link != "" || (ticket.From != "" && ticket.To != ""))
}

// WithZones returns the ticket with its zones taken from the zone sets found
// along its route. The route must lie in exactly one Verbund, or in the one
// named by the ticket's Net.
func (ticket Ticket) WithZones(found []tariff.ZoneSet) (Ticket, error) {
	merged := map[string][]string{}
	var nets []string
	for _, zoneSet := range found {
		if _, ok := merged[zoneSet.Net]; !ok {
			nets = append(nets, zoneSet.Net)
		}
		for _, zone := range zoneSet.Zones {
			if !slices.Contains(merged[zoneSet.Net], zone) {
				merged[zoneSet.Net] = append(merged[zoneSet.Net], zone)
			}
		}
	}
	net := canonicalNet(ticket.Net)
	switch {
	case len(nets) == 0:
		return ticket, errors.New("no Verbund zones found for the ticket's route (it may cross a Verbund border, or the Verbund is not covered)")
	case net != "":
		if _, ok := merged[net]; !ok {
			return ticket, fmt.Errorf("the ticket's route has no zones in Verbund %q, found: %s", ticket.Net, strings.Join(displayNets(nets), ", "))
		}
	case len(nets) > 1:
		return ticket, fmt.Errorf("the ticket's route touches several Verbünde (%s), set net to choose one", strings.Join(displayNets(nets), ", "))
	default:
		net = nets[0]
	}
	ticket.Net, ticket.Zones = DisplayNet(net), merged[net]
	return ticket, nil
}

// DisplayNet maps a planner net code back to the name users know.
func DisplayNet(code string) string {
	for alias, netCode := range rules.NetAliases {
		if netCode == code {
			return alias
		}
	}
	return code
}

func displayNets(codes []string) []string {
	out := make([]string, len(codes))
	for index, code := range codes {
		out[index] = DisplayNet(code)
	}
	return out
}

// Verdict is the outcome for a leg or a whole journey.
type Verdict string

const (
	Valid       Verdict = "valid"
	Invalid     Verdict = "invalid"
	Unknown     Verdict = "unknown"
	NotRequired Verdict = "not_required" // walks
	// Partial: the ticket covers only a part of the leg or journey.
	Partial Verdict = "partial"
)

// LegResult is the outcome for one leg.
type LegResult struct {
	Leg     int     `json:"leg"`
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason"`
	// ValidFrom and ValidTo name the stops between which the ticket is
	// valid when the verdict is "partial".
	ValidFrom string `json:"validFrom,omitempty"`
	ValidTo   string `json:"validTo,omitempty"`
}

// Result is the outcome for one ticket on a journey.
type Result struct {
	Ticket  Ticket      `json:"ticket"`
	Verdict Verdict     `json:"verdict"`
	Legs    []LegResult `json:"legs"`
}

//go:embed tickets.json
var ticketsJSON []byte

type laenderticket struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	ProductPattern   string `json:"productPattern"`
	WeekdayStartHour int    `json:"weekdayStartHour"`
	NationwideTrains bool   `json:"nationwideTrains"`
	product          *regexp.Regexp
}

var rules = func() (loaded struct {
	DeutschlandticketPattern string            `json:"deutschlandticketPattern"`
	NetAliases               map[string]string `json:"netAliases"`
	Verbuende                []Option          `json:"verbuende"`
	Laendertickets           []*laenderticket  `json:"laendertickets"`
	dticket                  *regexp.Regexp
}) {
	if err := json.Unmarshal(ticketsJSON, &loaded); err != nil {
		panic("check: invalid embedded tickets.json: " + err.Error())
	}
	loaded.dticket = regexp.MustCompile(loaded.DeutschlandticketPattern)
	for _, stateTicket := range loaded.Laendertickets {
		stateTicket.product = regexp.MustCompile(stateTicket.ProductPattern)
	}
	return loaded
}()

// Option is one choice a user has when describing a ticket.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// LaenderticketOptions lists the supported names for type "laenderticket".
func LaenderticketOptions() []Option {
	var options []Option
	for _, stateTicket := range rules.Laendertickets {
		options = append(options, Option{ID: stateTicket.ID, Label: stateTicket.Label})
	}
	return options
}

// VerbundOptions lists the Verbünde offered for type "verbund"; any other
// net code is accepted too.
func VerbundOptions() []Option {
	return rules.Verbuende
}

// LaenderticketLabel returns the display name for a "laenderticket" name.
func LaenderticketLabel(name string) string {
	if stateTicket := findLaenderticket(name); stateTicket != nil {
		return stateTicket.Label
	}
	return name
}

func findLaenderticket(name string) *laenderticket {
	for _, stateTicket := range rules.Laendertickets {
		if stateTicket.ID == strings.ToLower(strings.TrimSpace(name)) {
			return stateTicket
		}
	}
	return nil
}

// Validate reports whether the ticket definition is usable.
func Validate(ticket Ticket) error {
	switch ticket.Type {
	case TypeDeutschlandticket:
		return nil
	case TypeLaenderticket:
		if findLaenderticket(ticket.Name) == nil {
			var names []string
			for _, stateTicket := range rules.Laendertickets {
				names = append(names, stateTicket.ID)
			}
			return fmt.Errorf("unknown laenderticket name %q, supported: %s", ticket.Name, strings.Join(names, ", "))
		}
		return nil
	case TypeVerbund:
		if ticket.NeedsZoneLookup() {
			return nil
		}
		if strings.TrimSpace(ticket.Net) == "" {
			return errors.New("verbund ticket needs a net, or its route as from/to or link")
		}
		return nil
	}
	return fmt.Errorf("unknown ticket type %q, supported: %s, %s, %s", ticket.Type, TypeDeutschlandticket, TypeLaenderticket, TypeVerbund)
}

// Check evaluates the ticket for every leg. assessments must be parallel to
// connection.Legs; walks may carry a zero Assessment.
func Check(ticket Ticket, connection journey.Journey, assessments []tariff.Assessment) Result {
	res := Result{Ticket: ticket}
	for index, leg := range connection.Legs {
		var assessment tariff.Assessment
		if index < len(assessments) {
			assessment = assessments[index]
		}
		legResult := LegResult{Leg: leg.Index}
		legResult.Verdict, legResult.Reason = checkLeg(ticket, leg, assessment)
		res.Legs = append(res.Legs, legResult)
	}
	res.Verdict = overall(res.Legs)
	return res
}

func checkLeg(ticket Ticket, leg journey.Leg, assessment tariff.Assessment) (Verdict, string) {
	if leg.Walk {
		return NotRequired, "Fußweg, kein Ticket nötig"
	}
	switch ticket.Type {
	case TypeDeutschlandticket:
		return deutschlandticket(leg, assessment)
	case TypeLaenderticket:
		return laender(findLaenderticket(ticket.Name), leg, assessment)
	case TypeVerbund:
		return verbund(ticket, leg, assessment)
	}
	return Unknown, "unbekannter Tickettyp"
}

// overall combines leg verdicts: "partial" as soon as the ticket covers
// some but not all of the journey.
func overall(legs []LegResult) Verdict {
	var valid, partial, invalid, unknown bool
	for _, legResult := range legs {
		switch legResult.Verdict {
		case Valid:
			valid = true
		case Partial:
			partial = true
		case Invalid:
			invalid = true
		case Unknown:
			unknown = true
		}
	}
	switch {
	case partial || (invalid && valid):
		return Partial
	case invalid:
		return Invalid
	case unknown:
		return Unknown
	case valid:
		return Valid
	}
	return NotRequired
}

// Recurring parts of the reasons.
const reasonAbroad = "Start oder Ziel liegt im Ausland; Grenzabschnitte sind nicht erfasst"

// bySource names the fare source an answer rests on.
func bySource(assessment tariff.Assessment) string {
	return "(Tarifauskunft " + assessment.Source + ")"
}

// noAssessment is the reason when no fare source knows the ride.
func noAssessment(assessment tariff.Assessment) string {
	return "keine Tarifauskunft für diesen Abschnitt: " + assessment.Note
}

// fernverkehr decides whether a leg is barred as long-distance transport.
// A long-distance train the fare source also knows under a local line
// (a.LocalLine) is not barred. An IC/EC no source could look at stays
// "unknown", because it might be such a train; an ICE never is.
func fernverkehr(leg journey.Leg, assessment tariff.Assessment) (barred bool, verdict Verdict, reason string) {
	if !leg.LongDistance() || assessment.LocalLine != "" {
		return false, "", ""
	}
	if leg.Product == journey.ProductICE || assessment.Matched {
		return true, Invalid, "Fernverkehr (" + lineName(leg) + ") ist nicht enthalten"
	}
	return true, Unknown, lineName(leg) + " ist Fernverkehr; ob der Zug auch als Nahverkehrszug fährt, konnte nicht geprüft werden: " + assessment.Note
}

// alsoLocal is the reason prefix for long-distance trains with a local twin.
func alsoLocal(leg journey.Leg, assessment tariff.Assessment) string {
	if leg.LongDistance() && assessment.LocalLine != "" {
		return lineName(leg) + " fährt auch als " + assessment.LocalLine + " (Nahverkehr); "
	}
	return ""
}

func deutschlandticket(leg journey.Leg, assessment tariff.Assessment) (Verdict, string) {
	// Decided before looking at the products: some planners list the
	// Deutschlandticket even for ICE rides.
	if barred, verdict, reason := fernverkehr(leg, assessment); barred {
		return verdict, reason
	}
	if abroad(leg) {
		return Unknown, reasonAbroad
	}
	if assessment.Matched && offers(assessment, rules.dticket) {
		return Valid, alsoLocal(leg, assessment) + "Nahverkehr; von der Tarifauskunft " + assessment.Source + " bestätigt"
	}
	if leg.LongDistance() {
		// Local twin, but the source does not offer the ticket for it.
		return Unknown, alsoLocal(leg, assessment) + "das Deutschlandticket ist dafür aber nicht bestätigt " + bySource(assessment)
	}
	if localScheduled(leg) {
		return Valid, "Nahverkehr; gilt nach Regel, nicht durch eine Tarifauskunft bestätigt"
	}
	return Unknown, "Verkehrsmittel " + leg.Product + " ist nicht eindeutig im Deutschlandticket enthalten"
}

func laender(stateTicket *laenderticket, leg journey.Leg, assessment tariff.Assessment) (Verdict, string) {
	if stateTicket == nil {
		return Unknown, "unbekanntes Länderticket"
	}
	if barred, verdict, reason := fernverkehr(leg, assessment); barred {
		return verdict, reason
	}
	if !withinValidity(stateTicket, leg.Departure) {
		return Invalid, fmt.Sprintf("%s gilt Mo–Fr erst ab %d Uhr (Feiertage sind nicht berücksichtigt)", stateTicket.Label, stateTicket.WeekdayStartHour)
	}
	if assessment.Matched && offers(assessment, stateTicket.product) {
		return Valid, alsoLocal(leg, assessment) + "von der Tarifauskunft " + assessment.Source + " bestätigt"
	}
	if stateTicket.NationwideTrains {
		if abroad(leg) {
			return Unknown, reasonAbroad
		}
		if leg.Product == journey.ProductRegional || leg.Product == journey.ProductSBahn {
			return Valid, "Nahverkehrszug; gilt nach Regel, nicht durch eine Tarifauskunft bestätigt"
		}
		return Unknown, "gilt in Nahverkehrszügen; für " + leg.Product + " nicht bestätigt"
	}
	if !assessment.Matched {
		return Unknown, noAssessment(assessment)
	}
	return Unknown, "Geltungsbereich des " + stateTicket.Label + " für diesen Abschnitt nicht bestätigt " + bySource(assessment)
}

func verbund(ticket Ticket, leg journey.Leg, assessment tariff.Assessment) (Verdict, string) {
	ticket = ticket.Normalized()
	if barred, verdict, reason := fernverkehr(leg, assessment); barred {
		return verdict, reason
	}
	net := canonicalNet(ticket.Net)
	if !assessment.Matched {
		return Unknown, noAssessment(assessment)
	}
	for _, zoneSet := range assessment.Zones {
		if zoneSet.Net != net {
			continue
		}
		if ticket.Network {
			return Valid, "Abschnitt liegt im Verbund " + ticket.Net + " " + bySource(assessment)
		}
		// The planner may give the zones in two notations.
		for index, need := range [][]string{zoneSet.Zones, zoneSet.ZonesUnited} {
			if (index == 0 || len(need) > 0) && len(missingZones(ticket.Zones, need)) == 0 {
				return Valid, "benötigte Zonen " + strings.Join(need, ", ") + " sind abgedeckt " + bySource(assessment)
			}
		}
		missing := missingZones(ticket.Zones, zoneSet.Zones)
		return Invalid, "Zone(n) " + strings.Join(missing, ", ") + " fehlen; benötigt: " + strings.Join(zoneSet.Zones, ", ") + " " + bySource(assessment)
	}
	if slices.Contains(assessment.KnownNets, net) {
		return Invalid, "Abschnitt liegt laut Tarifauskunft " + assessment.Source + " nicht vollständig im Verbund " + ticket.Net
	}
	return Unknown, "Tarifauskunft " + assessment.Source + " liefert keine Zonen für den Verbund " + ticket.Net
}

// canonicalNet maps a user-facing Verbund name to the planner's net code.
func canonicalNet(net string) string {
	net = strings.ToLower(strings.TrimSpace(net))
	if code, ok := rules.NetAliases[net]; ok {
		return code
	}
	return net
}

func missingZones(have, need []string) []string {
	var missing []string
	for _, needed := range need {
		if !slices.ContainsFunc(have, func(held string) bool { return strings.EqualFold(strings.TrimSpace(held), needed) }) {
			missing = append(missing, needed)
		}
	}
	return missing
}

func offers(assessment tariff.Assessment, pattern *regexp.Regexp) bool {
	return slices.ContainsFunc(assessment.Products, func(product tariff.Product) bool { return pattern.MatchString(product.Name) })
}

// withinValidity applies the day-ticket rule: Mon–Fri from the start hour
// until 3:00 the next day, all day on weekends.
func withinValidity(stateTicket *laenderticket, dep time.Time) bool {
	hour := dep.Hour()
	if hour < 3 {
		// Counts as the tail of the previous day's ticket.
		return true
	}
	if weekday := dep.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
		return true
	}
	return hour >= stateTicket.WeekdayStartHour
}

// localScheduled reports whether the product is ordinary local transport.
func localScheduled(leg journey.Leg) bool {
	switch leg.Product {
	case journey.ProductRegional, journey.ProductSBahn, journey.ProductUBahn, journey.ProductTram, journey.ProductBus:
		return true
	}
	return false
}

// abroad reports whether an endpoint is a stop outside Germany. Two kinds
// of ids tell: EVA numbers (seven digits, German ones start with 80) and
// station ids with a country prefix ("de:08335:6554", "ch:1:sloid:6131").
func abroad(leg journey.Leg) bool {
	for _, id := range []string{leg.Origin.ID, leg.Destination.ID} {
		if len(id) == 7 && !strings.HasPrefix(id, "80") && strings.Trim(id, "0123456789") == "" {
			return true
		}
		if country, _, ok := strings.Cut(id, ":"); ok && len(country) == 2 && country != "de" {
			return true
		}
	}
	return false
}

func lineName(leg journey.Leg) string {
	if leg.Line != "" {
		return leg.Line
	}
	return leg.Product
}
