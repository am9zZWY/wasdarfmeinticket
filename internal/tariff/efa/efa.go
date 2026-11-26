// Package efa asks EFA journey planners (the software behind most German
// Verbund planners) for the tariff of a single ride.
package efa

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/httpx"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/progress"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

//go:embed planners.json
var plannersJSON []byte

// maxStopDistance is how far a planner stop may be from the DB stop to be
// considered the same place.
const maxStopDistance = 1500.0 // metres

// BBox is a latitude/longitude rectangle.
type BBox = tariff.BBox

// Planner is one EFA installation.
type Planner struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseURL"`
	// Nets are the Verbund codes this planner is verified to report zones for.
	Nets []string `json:"nets"`
	BBox BBox     `json:"bbox"`
	// Browser is set when the planner allows cross-origin requests, so the
	// browser app can ask it directly.
	Browser bool `json:"browser"`
}

// DefaultPlanners returns the built-in planner list, most specific first.
func DefaultPlanners() []Planner {
	var planners []Planner
	if err := json.Unmarshal(plannersJSON, &planners); err != nil {
		panic("efa: invalid embedded planners.json: " + err.Error())
	}
	return planners
}

// Engine implements tariff.Source on top of a list of planners.
type Engine struct {
	Planners []Planner
	HTTP     *httpx.Client
	Location *time.Location // time zone the planners expect request times in
	// Cache, if set, keeps a planner's answer for a line between two
	// stops. The answer does not depend on the time of day, so other
	// departures of the same line reuse it.
	Cache *cache.Cache

	mutex  sync.Mutex
	stops  map[string]string    // planner + DB stop -> planner stop id
	paused map[string]time.Time // planner -> not to be asked before
}

// rateLimitPause is how long a planner is left alone after it answered
// "too many requests".
const rateLimitPause = time.Minute

var errPaused = errors.New("pausiert nach Ratenbegrenzung (HTTP 429)")

// usable reports whether the planner may be asked right now.
func (engine *Engine) usable(planner Planner) bool {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	return time.Now().After(engine.paused[planner.Name])
}

// noteError pauses the planner if err is a rate limit.
func (engine *Engine) noteError(planner Planner, err error) {
	if !httpx.IsStatus(err, http.StatusTooManyRequests) {
		return
	}
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	if engine.paused == nil {
		engine.paused = map[string]time.Time{}
	}
	engine.paused[planner.Name] = time.Now().Add(rateLimitPause)
}

// New returns an engine using the built-in planners.
func New(loc *time.Location) *Engine {
	return &Engine{Planners: DefaultPlanners(), HTTP: httpx.New(), Location: loc}
}

// Assess asks every planner covering the leg and merges their answers.
// Planner areas overlap (their bounding boxes are coarse, and planners know
// trains beyond the Verbünde they have tariffs for), so the first answer is
// not necessarily the one carrying the zones.
func (engine *Engine) Assess(ctx context.Context, leg journey.Leg) tariff.Assessment {
	if !leg.Origin.HasCoord() || !leg.Destination.HasCoord() {
		return tariff.Assessment{Note: "stop positions unknown, cannot select a tariff planner"}
	}
	var covering []Planner
	for _, planner := range engine.Planners {
		if planner.BBox.Covers(leg) {
			covering = append(covering, planner)
		}
	}
	if len(covering) == 0 {
		return tariff.Assessment{Note: tariff.NotCovered}
	}
	answers := make([]tariff.Assessment, len(covering))
	errs := make([]error, len(covering))
	var waitGroup sync.WaitGroup
	for index, planner := range covering {
		waitGroup.Go(func() {
			if !engine.usable(planner) {
				errs[index] = errPaused
				return
			}
			answers[index], errs[index] = engine.assess(ctx, planner, leg)
			engine.noteError(planner, errs[index])
		})
	}
	waitGroup.Wait()

	var out tariff.Assessment
	var sources, notes []string
	for index, assessment := range answers {
		if errs[index] != nil {
			if errs[index] != errPaused {
				progress.Logf(ctx, "%s: %s: keine Auskunft (%v)", covering[index].Name, leg, errs[index])
			}
			notes = append(notes, covering[index].Name+": "+errs[index].Error())
			continue
		}
		out.Matched = true
		sources = append(sources, assessment.Source)
		if out.LocalLine == "" {
			out.LocalLine = assessment.LocalLine
		}
		out.KnownNets = append(out.KnownNets, assessment.KnownNets...)
		out.Zones = append(out.Zones, assessment.Zones...)
		for _, product := range assessment.Products {
			if !slices.Contains(out.Products, product) {
				out.Products = append(out.Products, product)
			}
		}
	}
	out.Source = strings.Join(sources, " + ")
	// Keep failures visible even when another planner answered.
	out.Note = strings.Join(notes, "; ")
	return out
}

// tariffTTL is how long a planner's answer is reused. Tariffs change a few
// times a year at most.
const tariffTTL = 7 * 24 * time.Hour

// cacheKey identifies a ride independently of its time: planner, line and
// both stops. A leg without a line name is identified by its train number
// only, which differs from run to run, so it is not cached.
func cacheKey(planner Planner, leg journey.Leg) (string, bool) {
	if leg.Line == "" {
		return "", false
	}
	stop := func(stop journey.Stop) string {
		if stop.ID != "" {
			return stop.ID
		}
		return stop.Name
	}
	return "tariff|" + planner.Name + "|" + leg.Line + "|" + stop(leg.Origin) + "|" + stop(leg.Destination), true
}

func (engine *Engine) assess(ctx context.Context, planner Planner, leg journey.Leg) (tariff.Assessment, error) {
	key, cacheable := cacheKey(planner, leg)
	if cacheable {
		var assessment tariff.Assessment
		if engine.Cache.Get(key, &assessment) {
			progress.Logf(ctx, "%s: %s: aus dem Cache: %s", planner.Name, leg, summary(assessment))
			return assessment, nil
		}
	}
	progress.Logf(ctx, "%s: Tarif für %s abfragen", planner.Name, leg)
	from, err := engine.stopID(ctx, planner, leg.Origin)
	if err != nil {
		return tariff.Assessment{}, err
	}
	to, err := engine.stopID(ctx, planner, leg.Destination)
	if err != nil {
		return tariff.Assessment{}, err
	}
	dep := leg.Departure.In(engine.Location)
	query := url.Values{
		"outputFormat":          {"rapidJSON"},
		"version":               {"10.4.18.18"},
		"type_origin":           {"any"},
		"name_origin":           {from},
		"type_destination":      {"any"},
		"name_destination":      {to},
		"itdDate":               {dep.Format("20060102")},
		"itdTime":               {dep.Format("1504")},
		"itdTripDateTimeDepArr": {"dep"},
		"calcNumberOfTrips":     {"4"},
	}
	var resp tripResponse
	if err := engine.HTTP.GetJSON(ctx, planner.BaseURL+"XML_TRIP_REQUEST2?"+query.Encode(), &resp); err != nil {
		return tariff.Assessment{}, err
	}
	assessment, ok := extract(resp, leg)
	if !ok {
		return tariff.Assessment{}, fmt.Errorf("ride %s %s not found in planner results", leg.Line, leg.Number)
	}
	assessment.Source = planner.Name
	assessment.KnownNets = planner.Nets
	progress.Logf(ctx, "%s: %s: %s", planner.Name, leg, summary(assessment))
	if cacheable {
		engine.Cache.Set(key, assessment, tariffTTL)
	}
	return assessment, nil
}

type tripResponse struct {
	Journeys []struct {
		Legs []efaLeg `json:"legs"`
		Fare struct {
			Tickets []struct {
				Name    string `json:"name"`
				Net     string `json:"net"`
				FromLeg int    `json:"fromLeg"`
				ToLeg   int    `json:"toLeg"`
			} `json:"tickets"`
			Zones []struct {
				Net         string     `json:"net"`
				FromLeg     int        `json:"fromLeg"`
				ToLeg       int        `json:"toLeg"`
				Zones       [][]string `json:"zones"`
				ZonesUnited [][]string `json:"zonesUnited"`
			} `json:"zones"`
		} `json:"fare"`
	} `json:"journeys"`
}

type efaLeg struct {
	Origin struct {
		Departure time.Time `json:"departureTimePlanned"`
	} `json:"origin"`
	Destination struct {
		Arrival time.Time `json:"arrivalTimePlanned"`
	} `json:"destination"`
	Transportation struct {
		Number  string `json:"number"`
		Product struct {
			Class int `json:"class"`
		} `json:"product"`
		Properties struct {
			TrainNumber string `json:"trainNumber"`
		} `json:"properties"`
	} `json:"transportation"`
}

// isRide reports whether the planner leg is a vehicle (not a footpath or
// similar individual transport, product classes 99 and above).
func (plannerLeg efaLeg) isRide() bool { return plannerLeg.Transportation.Product.Class < 99 }

// isLocal reports whether the planner classifies the ride as local
// transport: S-Bahn up to regional trains are classes 1 to 13, long-distance
// trains 14 and above (IC 15, ICE 16), and 0 is an unspecified train.
func (plannerLeg efaLeg) isLocal() bool {
	class := plannerLeg.Transportation.Product.Class
	return class >= 1 && class <= 13
}

// extract finds the journey that consists of exactly the given ride and
// returns its fare information.
//
// Some trains carry two designations, for example an IC that is also
// released as a regional train. bahn.de then reports the IC, while planners
// list the ride twice: once as the IC without local fares and once as the
// regional train (other train number) with them. For a long-distance leg
// the local twin, recognised by identical departure and arrival times, is
// therefore preferred.
func extract(resp tripResponse, leg journey.Leg) (tariff.Assessment, bool) {
	exact, twin := -1, -1
	for journeyIndex, plannerJourney := range resp.Journeys {
		ride, ok := onlyRide(plannerJourney.Legs)
		if !ok {
			continue
		}
		plannerLeg := plannerJourney.Legs[ride]
		if exact < 0 && sameRide(plannerLeg, leg) {
			exact = journeyIndex
		}
		if twin < 0 && leg.LongDistance() && plannerLeg.isLocal() && sameTimes(plannerLeg, leg) {
			twin = journeyIndex
		}
	}
	journeyIndex := exact
	if twin >= 0 {
		journeyIndex = twin
	}
	if journeyIndex < 0 {
		return tariff.Assessment{}, false
	}
	plannerJourney := resp.Journeys[journeyIndex]
	ride, _ := onlyRide(plannerJourney.Legs)
	assessment := tariff.Assessment{Matched: true}
	if plannerLeg := plannerJourney.Legs[ride]; plannerLeg.isLocal() {
		assessment.LocalLine = plannerLeg.Transportation.Number
	}
	for _, fareZones := range plannerJourney.Fare.Zones {
		if ride < fareZones.FromLeg || ride > fareZones.ToLeg {
			continue
		}
		assessment.Zones = append(assessment.Zones, tariff.ZoneSet{
			Net:         strings.ToLower(fareZones.Net),
			Zones:       flatten(fareZones.Zones),
			ZonesUnited: flatten(fareZones.ZonesUnited),
		})
	}
	for _, fareTicket := range plannerJourney.Fare.Tickets {
		if ride < fareTicket.FromLeg || ride > fareTicket.ToLeg {
			continue
		}
		if product := (tariff.Product{Net: strings.ToLower(fareTicket.Net), Name: fareTicket.Name}); !slices.Contains(assessment.Products, product) {
			assessment.Products = append(assessment.Products, product)
		}
	}
	return assessment, true
}

// onlyRide returns the index of the single vehicle leg of a planner journey,
// and false if it has none or several.
func onlyRide(legs []efaLeg) (int, bool) {
	ride, rides := -1, 0
	for index, plannerLeg := range legs {
		if plannerLeg.isRide() {
			ride = index
			rides++
		}
	}
	return ride, rides == 1
}

// sameRide compares planned departure and, if both sides know it, the train
// number. Times alone are accepted for vehicles without a number (buses).
func sameRide(plannerLeg efaLeg, leg journey.Leg) bool {
	if !plannerLeg.Origin.Departure.Equal(leg.Departure) {
		return false
	}
	if number := plannerLeg.Transportation.Properties.TrainNumber; number != "" && leg.Number != "" {
		return number == leg.Number
	}
	return sameTimes(plannerLeg, leg)
}

func sameTimes(plannerLeg efaLeg, leg journey.Leg) bool {
	return plannerLeg.Origin.Departure.Equal(leg.Departure) && plannerLeg.Destination.Arrival.Equal(leg.Arrival)
}

// summary describes an assessment in one line for progress messages.
func summary(assessment tariff.Assessment) string {
	var parts []string
	for _, zoneSet := range assessment.Zones {
		parts = append(parts, "Zonen "+zoneSet.Net+" "+strings.Join(zoneSet.Zones, ", "))
	}
	if len(parts) == 0 {
		parts = append(parts, "keine Verbundzonen")
	}
	parts = append(parts, fmt.Sprintf("%d Ticketprodukte", len(assessment.Products)))
	if assessment.LocalLine != "" {
		parts = append(parts, "Linie "+assessment.LocalLine)
	}
	return strings.Join(parts, "; ")
}

func flatten(groups [][]string) []string {
	var out []string
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

type stopFinderResponse struct {
	Locations []struct {
		ID    string    `json:"id"`
		Coord []float64 `json:"coord"` // lat, lon
	} `json:"locations"`
}

// stopID maps a DB stop to the planner's stop id: the candidate by name
// that lies closest to the DB position.
func (engine *Engine) stopID(ctx context.Context, planner Planner, stop journey.Stop) (string, error) {
	// Stops from the timetable source carry the nationwide station id
	// (DHID), which the planners accept directly.
	if strings.HasPrefix(stop.ID, "de:") {
		return stop.ID, nil
	}
	key := planner.Name + "|" + stop.ID + "|" + stop.Name
	engine.mutex.Lock()
	id, ok := engine.stops[key]
	engine.mutex.Unlock()
	if ok {
		return id, nil
	}
	query := url.Values{
		"outputFormat":      {"rapidJSON"},
		"version":           {"10.4.18.18"},
		"type_sf":           {"any"},
		"name_sf":           {stop.Name},
		"anyObjFilter_sf":   {"2"}, // stops only
		"coordOutputFormat": {"WGS84[dd.ddddd]"},
	}
	var resp stopFinderResponse
	if err := engine.HTTP.GetJSON(ctx, planner.BaseURL+"XML_STOPFINDER_REQUEST?"+query.Encode(), &resp); err != nil {
		return "", err
	}
	best, bestDist := "", math.Inf(1)
	for _, location := range resp.Locations {
		if len(location.Coord) != 2 {
			continue
		}
		if distance := stop.DistanceTo(journey.Stop{Lat: location.Coord[0], Lon: location.Coord[1]}); distance < bestDist {
			best, bestDist = location.ID, distance
		}
	}
	if best == "" || bestDist > maxStopDistance {
		return "", fmt.Errorf("stop %q not found", stop.Name)
	}
	engine.mutex.Lock()
	if engine.stops == nil {
		engine.stops = map[string]string{}
	}
	engine.stops[key] = best
	engine.mutex.Unlock()
	return best, nil
}
