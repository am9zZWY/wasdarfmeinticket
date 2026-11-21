package check

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

var berlin = func() *time.Location {
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	return location
}()

// 2026-11-24 is a Tuesday, 2026-11-28 a Saturday.
func at(day, hour int) time.Time { return time.Date(2026, 11, day, hour, 30, 0, 0, berlin) }

func leg(product string, dep time.Time) journey.Leg {
	return journey.Leg{
		Product: product, Line: product, Departure: dep,
		Origin: journey.Stop{ID: "8000141"}, Destination: journey.Stop{ID: "8000096"},
	}
}

var (
	// Assessments mirror the recorded planner answers.
	naldo = tariff.Assessment{Matched: true, Source: "EFA-BW", KnownNets: []string{"tub", "vhb"},
		Zones: []tariff.ZoneSet{{Net: "tub", Zones: []string{"11", "220"}}},
		Products: []tariff.Product{{Net: "tub", Name: "Deutschlandticket (persönlich)"},
			{Net: "bwt", Name: "Baden-Württemberg-Ticket, 2. Klasse, 1 Reisender"}}}
	border = tariff.Assessment{Matched: true, Source: "EFA-BW", KnownNets: []string{"tub", "vhb"},
		Products: []tariff.Product{{Net: "nvbw", Name: "Deutschland-Ticket"},
			{Net: "bwt", Name: "Baden-Württemberg-Ticket, 2. Klasse, 1 Reisender"}}}
	vgn = tariff.Assessment{Matched: true, Source: "Bahnland Bayern", KnownNets: []string{"vgn"},
		Zones: []tariff.ZoneSet{{Net: "vgn", Zones: []string{"100", "200", "397", "398", "400"},
			ZonesUnited: []string{"100", "200", "300", "400"}}},
		Products: []tariff.Product{{Net: "dtv", Name: "Bayern Ticket Tag"}, {Net: "dtv", Name: "Bayern-Böhmen-Ticket"}}}
	mvv = tariff.Assessment{Matched: true, Source: "MVV", KnownNets: []string{"mvv"},
		Zones: []tariff.ZoneSet{{Net: "mvv", Zones: []string{"M", "1", "2", "3", "4"}}}}
	// The Bavaria planner lists a Deutschlandticket even for ICE rides.
	iceWithDTicket = tariff.Assessment{Matched: true, Source: "Bahnland Bayern",
		Products: []tariff.Product{{Net: "vlk", Name: "Deutschlandticket"}}}
	noMatch = tariff.Assessment{Note: "ride not found"}
	// An IC the BW planner also lists as the regional train RE 87.
	icAsRE = tariff.Assessment{Matched: true, Source: "EFA-BW", KnownNets: []string{"tub", "vhb"}, LocalLine: "RE 87",
		Products: []tariff.Product{{Net: "nvbw", Name: "Deutschland-Ticket"},
			{Net: "bwt", Name: "Baden-Württemberg-Ticket, 2. Klasse, 1 Reisender"}}}
	// The same twin, but without any ticket offered for it.
	icAsRENoProducts = tariff.Assessment{Matched: true, Source: "EFA-BW", LocalLine: "RE 87"}
	// An IC the planner knows as long-distance only.
	icOnly = tariff.Assessment{Matched: true, Source: "EFA-BW", KnownNets: []string{"tub", "vhb"}}
)

func TestCheckLeg(t *testing.T) {
	dticket := Ticket{Type: TypeDeutschlandticket}
	bwTicket := Ticket{Type: TypeLaenderticket, Name: "baden-wuerttemberg-ticket"}
	bayernTicket := Ticket{Type: TypeLaenderticket, Name: "bayern-ticket"}
	qdl := Ticket{Type: TypeLaenderticket, Name: "quer-durchs-land-ticket"}
	zone := func(net string, zones ...string) Ticket { return Ticket{Type: TypeVerbund, Net: net, Zones: zones} }

	tests := []struct {
		name       string
		ticket     Ticket
		leg        journey.Leg
		assessment tariff.Assessment
		want       Verdict
	}{
		{"D-Ticket confirmed by engine", dticket, leg(journey.ProductRegional, at(24, 9)), naldo, Valid},
		{"D-Ticket by rule without engine", dticket, leg(journey.ProductSBahn, at(24, 9)), noMatch, Valid},
		{"D-Ticket on ICE", dticket, leg(journey.ProductICE, at(24, 9)), noMatch, Invalid},
		{"D-Ticket on ICE despite engine listing it", dticket, leg(journey.ProductICE, at(24, 9)), iceWithDTicket, Invalid},
		{"D-Ticket on IC the planner knows as IC only", dticket, leg(journey.ProductECIC, at(24, 9)), icOnly, Invalid},
		{"D-Ticket on IC nobody could look at", dticket, leg(journey.ProductECIC, at(24, 9)), noMatch, Unknown},
		{"D-Ticket on IC that is also an RE", dticket, leg(journey.ProductECIC, at(24, 9)), icAsRE, Valid},
		{"D-Ticket on IC/RE twin without confirmation", dticket, leg(journey.ProductECIC, at(24, 9)), icAsRENoProducts, Unknown},
		{"BW-Ticket on IC that is also an RE", bwTicket, leg(journey.ProductECIC, at(24, 10)), icAsRE, Valid},
		{"BW-Ticket on IC/RE twin before 9", bwTicket, leg(journey.ProductECIC, at(24, 8)), icAsRE, Invalid},
		{"Bayern-Ticket on IC/RE twin in BW", bayernTicket, leg(journey.ProductECIC, at(24, 10)), icAsRE, Unknown},
		{"naldo ticket on IC/RE twin outside naldo", zone("naldo", "11"), leg(journey.ProductECIC, at(24, 9)), icAsRE, Invalid},
		{"naldo ticket on ICE inside naldo", zone("naldo", "11", "220"), leg(journey.ProductICE, at(24, 9)), naldo, Invalid},
		{"naldo ticket on IC nobody could look at", zone("naldo", "11", "220"), leg(journey.ProductECIC, at(24, 9)), noMatch, Unknown},
		{"D-Ticket on ship", dticket, leg(journey.ProductShip, at(24, 9)), noMatch, Unknown},
		{"D-Ticket abroad", dticket, journey.Leg{Product: journey.ProductRegional, Departure: at(24, 9),
			Origin: journey.Stop{ID: "8000096"}, Destination: journey.Stop{ID: "8503000"}}, noMatch, Unknown},

		{"D-Ticket to a Swiss stop by station id", dticket, journey.Leg{Product: journey.ProductSBahn, Departure: at(24, 9),
			Origin: journey.Stop{ID: "de:08335:6554"}, Destination: journey.Stop{ID: "ch:1:sloid:6131"}}, noMatch, Unknown},
		{"D-Ticket between German stops by station id", dticket, journey.Leg{Product: journey.ProductSBahn, Departure: at(24, 9),
			Origin: journey.Stop{ID: "de:08335:6554"}, Destination: journey.Stop{ID: "de:08335:6588"}}, noMatch, Valid},
		{"BW-Ticket confirmed", bwTicket, leg(journey.ProductRegional, at(24, 9)), border, Valid},
		{"BW-Ticket before 9 on a weekday", bwTicket, leg(journey.ProductRegional, at(24, 8)), border, Invalid},
		{"BW-Ticket before 9 on Saturday", bwTicket, leg(journey.ProductRegional, at(28, 8)), border, Valid},
		{"BW-Ticket after midnight", bwTicket, leg(journey.ProductRegional, at(25, 1)), border, Valid},
		{"BW-Ticket on ICE", bwTicket, leg(journey.ProductICE, at(24, 10)), border, Invalid},
		{"BW-Ticket without engine", bwTicket, leg(journey.ProductRegional, at(24, 10)), noMatch, Unknown},
		{"BW-Ticket in Bavaria not confirmed", bwTicket, leg(journey.ProductRegional, at(24, 10)), vgn, Unknown},
		{"Bayern-Ticket confirmed", bayernTicket, leg(journey.ProductSBahn, at(24, 10)), vgn, Valid},
		{"Bayern-Ticket not confirmed by Bayern-Böhmen only", bayernTicket, leg(journey.ProductSBahn, at(24, 10)),
			tariff.Assessment{Matched: true, Source: "x", Products: []tariff.Product{{Name: "Bayern-Böhmen-Ticket"}, {Name: "Bayern Ticket Nacht"}}}, Unknown},
		{"QdL on regional train by rule", qdl, leg(journey.ProductRegional, at(24, 10)), noMatch, Valid},
		{"QdL on bus", qdl, leg(journey.ProductBus, at(24, 10)), noMatch, Unknown},
		{"QdL before 9", qdl, leg(journey.ProductRegional, at(24, 7)), noMatch, Invalid},

		{"naldo zones cover", zone("naldo", "11", "220"), leg(journey.ProductRegional, at(24, 9)), naldo, Valid},
		{"naldo zones via net code", zone("TUB", "220", "11", "12"), leg(journey.ProductRegional, at(24, 9)), naldo, Valid},
		{"naldo zone missing", zone("naldo", "11"), leg(journey.ProductRegional, at(24, 9)), naldo, Invalid},
		{"naldo network ticket", Ticket{Type: TypeVerbund, Net: "naldo", Network: true}, leg(journey.ProductRegional, at(24, 9)), naldo, Valid},
		{"naldo ticket across the border", zone("naldo", "11", "220"), leg(journey.ProductRegional, at(24, 9)), border, Invalid},
		{"naldo network ticket across the border", Ticket{Type: TypeVerbund, Net: "naldo", Network: true}, leg(journey.ProductRegional, at(24, 9)), border, Invalid},
		{"unverified net is unknown, not invalid", zone("vvs", "1"), leg(journey.ProductRegional, at(24, 9)), border, Unknown},
		{"verbund without engine", zone("naldo", "11", "220"), leg(journey.ProductRegional, at(24, 9)), noMatch, Unknown},
		{"vgn united zones", zone("vgn", "100", "200", "300", "400"), leg(journey.ProductSBahn, at(24, 9)), vgn, Valid},
		{"vgn detailed zones", zone("vgn", "100", "200", "397", "398", "400"), leg(journey.ProductSBahn, at(24, 9)), vgn, Valid},
		{"vgn zone missing", zone("vgn", "100", "200"), leg(journey.ProductSBahn, at(24, 9)), vgn, Invalid},
		{"mvv zones", zone("mvv", "m", "1", "2", "3", "4", "5"), leg(journey.ProductSBahn, at(24, 9)), mvv, Valid},
		{"mvv inner zone only", zone("mvv", "M"), leg(journey.ProductSBahn, at(24, 9)), mvv, Invalid},
	}
	// A regional twin of an IC inside the Verbund counts like any local train.
	icInNaldo := naldo
	icInNaldo.LocalLine = "RE 87"
	tests = append(tests, struct {
		name       string
		ticket     Ticket
		leg        journey.Leg
		assessment tariff.Assessment
		want       Verdict
	}{"naldo ticket on IC/RE twin inside naldo", zone("naldo", "11", "220"), leg(journey.ProductECIC, at(24, 9)), icInNaldo, Valid})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := Validate(testCase.ticket); err != nil {
				t.Fatal(err)
			}
			result := Check(testCase.ticket, journey.Journey{Legs: []journey.Leg{testCase.leg}}, []tariff.Assessment{testCase.assessment})
			if result.Verdict != testCase.want || result.Legs[0].Verdict != testCase.want {
				t.Errorf("verdict = %s (%s), want %s", result.Legs[0].Verdict, result.Legs[0].Reason, testCase.want)
			}
			if result.Legs[0].Reason == "" {
				t.Error("missing reason")
			}
		})
	}
}

func TestOverall(t *testing.T) {
	walk := journey.Leg{Index: 1, Walk: true}
	regionalLeg := leg(journey.ProductRegional, at(24, 9))
	ice := leg(journey.ProductICE, at(24, 11))
	ice.Index = 2
	deutschlandTicket := Ticket{Type: TypeDeutschlandticket}

	result := Check(deutschlandTicket, journey.Journey{Legs: []journey.Leg{regionalLeg, walk}}, []tariff.Assessment{naldo, {}})
	if result.Verdict != Valid || result.Legs[1].Verdict != NotRequired {
		t.Errorf("regional + walk: %+v", result)
	}
	result = Check(deutschlandTicket, journey.Journey{Legs: []journey.Leg{regionalLeg, walk, ice}}, []tariff.Assessment{naldo, {}, noMatch})
	if result.Verdict != Partial || result.Legs[0].Verdict != Valid || result.Legs[2].Verdict != Invalid {
		t.Errorf("regional + walk + ICE: %+v", result)
	}
	result = Check(deutschlandTicket, journey.Journey{Legs: []journey.Leg{ice}}, []tariff.Assessment{noMatch})
	if result.Verdict != Invalid {
		t.Errorf("ICE only: %+v", result)
	}
	twin := leg(journey.ProductECIC, at(24, 9))
	twin.Line = "IC 389"
	result = Check(deutschlandTicket, journey.Journey{Legs: []journey.Leg{twin}}, []tariff.Assessment{icAsRE})
	if result.Verdict != Valid || !strings.Contains(result.Legs[0].Reason, "IC 389 fährt auch als RE 87") {
		t.Errorf("IC/RE twin: %+v", result)
	}
	networkTicket := Ticket{Type: TypeVerbund, Net: "naldo", Network: true}
	result = Check(networkTicket, journey.Journey{Legs: []journey.Leg{regionalLeg, regionalLeg}}, []tariff.Assessment{naldo, noMatch})
	if result.Verdict != Unknown {
		t.Errorf("valid + unknown: %+v", result)
	}
	// Missing assessments must never turn into "valid" for zone tickets.
	result = Check(networkTicket, journey.Journey{Legs: []journey.Leg{regionalLeg}}, nil)
	if result.Verdict != Unknown {
		t.Errorf("no assessments: %+v", result)
	}
}

func TestWithZones(t *testing.T) {
	tub := tariff.ZoneSet{Net: "tub", Zones: []string{"11", "220"}}
	tub2 := tariff.ZoneSet{Net: "tub", Zones: []string{"220", "221"}}
	fds := tariff.ZoneSet{Net: "fds", Zones: []string{"14"}}
	route := Ticket{Type: TypeVerbund, From: "Tübingen Hbf", To: "Reutlingen Hbf"}
	if !route.NeedsZoneLookup() || Validate(route) != nil {
		t.Fatalf("route ticket should be valid and need a lookup")
	}
	if (Ticket{Type: TypeVerbund, Net: "naldo", Zones: []string{"11"}, From: "a", To: "b"}).NeedsZoneLookup() {
		t.Error("explicit zones must win over the route")
	}

	got, err := route.WithZones([]tariff.ZoneSet{tub, tub2})
	if err != nil || got.Net != "naldo" || !slices.Equal(got.Zones, []string{"11", "220", "221"}) || got.From != route.From {
		t.Errorf("single net: %+v, %v", got, err)
	}
	if got.NeedsZoneLookup() || Validate(got) != nil {
		t.Errorf("resolved ticket still needs a lookup: %+v", got)
	}
	if _, err := route.WithZones(nil); err == nil {
		t.Error("no zones: expected error")
	}
	if _, err := route.WithZones([]tariff.ZoneSet{tub, fds}); err == nil || !strings.Contains(err.Error(), "naldo, fds") {
		t.Errorf("two nets: err = %v", err)
	}
	withNet := route
	withNet.Net = "fds"
	if got, err := withNet.WithZones([]tariff.ZoneSet{tub, fds}); err != nil || got.Net != "fds" || !slices.Equal(got.Zones, []string{"14"}) {
		t.Errorf("net chosen: %+v, %v", got, err)
	}
	withNet.Net = "vhb"
	if _, err := withNet.WithZones([]tariff.ZoneSet{tub}); err == nil {
		t.Error("net not on route: expected error")
	}
}

func TestNetOnlyMeansWholeNetwork(t *testing.T) {
	netOnly := Ticket{Type: TypeVerbund, Net: "naldo"}
	if err := Validate(netOnly); err != nil {
		t.Fatal(err)
	}
	if normalized := netOnly.Normalized(); !normalized.Network {
		t.Errorf("normalized = %+v", normalized)
	}
	for _, other := range []Ticket{
		{Type: TypeVerbund, Net: "naldo", Zones: []string{"11"}},
		{Type: TypeVerbund, Net: "naldo", From: "a", To: "b"},
		{Type: TypeDeutschlandticket},
	} {
		if other.Normalized().Network {
			t.Errorf("%+v must not become a network ticket", other)
		}
	}
	// Checked without normalising first, it still behaves as a network ticket.
	connection := journey.Journey{Legs: []journey.Leg{leg(journey.ProductRegional, at(24, 9))}}
	if result := Check(netOnly, connection, []tariff.Assessment{naldo}); result.Verdict != Valid {
		t.Errorf("inside naldo: %+v", result)
	}
	if result := Check(netOnly, connection, []tariff.Assessment{border}); result.Verdict != Invalid {
		t.Errorf("across the border: %+v", result)
	}
}

func TestValidate(t *testing.T) {
	bad := []Ticket{
		{},
		{Type: "bahncard100"},
		{Type: TypeLaenderticket, Name: "hessenticket"},
		{Type: TypeVerbund, Zones: []string{"1"}},
		{Type: TypeVerbund, From: "Tübingen Hbf"},
	}
	for _, ticket := range bad {
		if Validate(ticket) == nil {
			t.Errorf("Validate(%+v): expected error", ticket)
		}
	}
}
