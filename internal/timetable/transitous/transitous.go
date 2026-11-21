// Package transitous reads timetable data from Transitous
// (https://transitous.org), a community-run routing service on open data;
// for Germany it uses the DELFI timetable feed (CC-BY-4.0).
//
// It supplies what bahn.de's share link does not: the intermediate stops of
// a ride with their times and nationwide stop ids, and connections between
// two stops. It has no fare data.
//
// Transitous asks API users to be open source, non-commercial, light on its
// resources and to identify themselves in the User-Agent header.
package transitous

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/httpx"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/progress"
)

// DefaultBaseURL is the public Transitous API.
const DefaultBaseURL = "https://api.transitous.org/api"

// DefaultUserAgent identifies this application to Transitous, as its usage
// policy requires: name, version and a contact.
const DefaultUserAgent = "wasdarfmeinticket/0.1 (Josef Müller; hello@jpkmiller.de)"

// UserAgentEnv names the environment variable that overrides the identification.
const UserAgentEnv = "WASDARFMEINTICKET_TRANSITOUS_USER_AGENT"

// maxWalkSeconds limits the walk between the given position and the first
// or last stop: enough to reach any platform of the station meant.
const maxWalkSeconds = "300"

// searchLead is how long before a departure the search starts: the planner
// walks from the given position to the platform first.
const searchLead = 10 * time.Minute

// Client talks to the Transitous API.
type Client struct {
	BaseURL string
	HTTP    *httpx.Client
	// Cache, if set, keeps stop lookups and the stop lists of rides.
	Cache *cache.Cache
}

const (
	stopTTL = 30 * 24 * time.Hour // a stop name resolves to the same stop
	rideTTL = 24 * time.Hour      // the stops of one particular ride
)

// New returns a client for the public Transitous API.
func New() *Client {
	httpClient := httpx.New()
	httpClient.UserAgent = DefaultUserAgent
	if userAgent := os.Getenv(UserAgentEnv); userAgent != "" {
		httpClient.UserAgent = userAgent
	}
	return &Client{BaseURL: DefaultBaseURL, HTTP: httpClient}
}

type planResponse struct {
	Itineraries []struct {
		Legs []planLeg `json:"legs"`
	} `json:"itineraries"`
}

type planLeg struct {
	Mode              string    `json:"mode"`
	From              place     `json:"from"`
	To                place     `json:"to"`
	ScheduledStart    time.Time `json:"scheduledStartTime"`
	ScheduledEnd      time.Time `json:"scheduledEndTime"`
	RouteShortName    string    `json:"routeShortName"`
	TripShortName     string    `json:"tripShortName"`
	TripID            string    `json:"tripId"`
	AgencyName        string    `json:"agencyName"`
	IntermediateStops []place   `json:"intermediateStops"`
}

type place struct {
	Name               string    `json:"name"`
	StopID             string    `json:"stopId"`
	Lat                float64   `json:"lat"`
	Lon                float64   `json:"lon"`
	ScheduledArrival   time.Time `json:"scheduledArrival"`
	ScheduledDeparture time.Time `json:"scheduledDeparture"`
}

// isRide reports whether the leg is a public transport vehicle.
func (plannerLeg planLeg) isRide() bool { return plannerLeg.TripID != "" }

// products maps Transitous modes to bahn.de's product names.
var products = map[string]string{
	"HIGHSPEED_RAIL":     journey.ProductICE,
	"LONG_DISTANCE":      journey.ProductECIC,
	"NIGHT_RAIL":         journey.ProductECIC,
	"COACH":              journey.ProductECIC, // long-distance coaches take no local tickets
	"REGIONAL_FAST_RAIL": journey.ProductRegional,
	"REGIONAL_RAIL":      journey.ProductRegional,
	"SUBURBAN":           journey.ProductSBahn,
	"METRO":              journey.ProductSBahn,
	"SUBWAY":             journey.ProductUBahn,
	"TRAM":               journey.ProductTram,
	"BUS":                journey.ProductBus,
	"FERRY":              journey.ProductShip,
}

func (client *Client) plan(ctx context.Context, from, to journey.Stop, when time.Time, extra url.Values) (planResponse, error) {
	query := url.Values{
		"fromPlace": {fmt.Sprintf("%.6f,%.6f", from.Lat, from.Lon)},
		"toPlace":   {fmt.Sprintf("%.6f,%.6f", to.Lat, to.Lon)},
		"time":      {when.UTC().Format(time.RFC3339)},
		// Start and destination are given as positions of stations. Without
		// a limit the planner may replace the last train by a long walk to
		// that position, and the journey would end at another stop.
		"maxPreTransitTime":  {maxWalkSeconds},
		"maxPostTransitTime": {maxWalkSeconds},
	}
	for key, values := range extra {
		query[key] = values
	}
	var resp planResponse
	if err := client.HTTP.GetJSON(ctx, client.BaseURL+"/v5/plan?"+query.Encode(), &resp); err != nil {
		return planResponse{}, fmt.Errorf("transitous: %w", err)
	}
	return resp, nil
}

// Enrich looks up the ride of a leg that is known only by its end stops and
// times, and returns the leg with its stop list (names, nationwide stop ids,
// positions, times). An empty line name is filled in as well. The ride is
// recognised by identical planned departure and arrival times.
func (client *Client) Enrich(ctx context.Context, leg journey.Leg) (journey.Leg, error) {
	if leg.Walk || !leg.Origin.HasCoord() || !leg.Destination.HasCoord() {
		return leg, fmt.Errorf("transitous: leg %d cannot be looked up", leg.Index)
	}
	key := fmt.Sprintf("ride|%s|%s|%d|%d", leg.Origin.Name, leg.Destination.Name, leg.Departure.Unix(), leg.Arrival.Unix())
	var rideInfo ride
	if client.Cache.Get(key, &rideInfo) && len(rideInfo.Stops) >= 2 {
		progress.Logf(ctx, "Transitous: %s: %d Halte aus dem Cache", leg, len(rideInfo.Stops))
		return rideInfo.fill(leg), nil
	}
	progress.Logf(ctx, "Transitous: Halte für %s suchen", leg)
	resp, err := client.plan(ctx, leg.Origin, leg.Destination, leg.Departure.Add(-searchLead), url.Values{"maxTransfers": {"0"}})
	if err != nil {
		progress.Logf(ctx, "Transitous: %s: Fehler: %v", leg, err)
		return leg, err
	}
	rideInfo, ok := findRide(resp, leg)
	if !ok {
		progress.Logf(ctx, "Transitous: %s: Fahrt nicht gefunden", leg)
		return leg, fmt.Errorf("transitous: ride %s %s at %s not found", leg.Line, leg.Number, leg.Departure.Format("15:04"))
	}
	progress.Logf(ctx, "Transitous: %s: %d Halte", leg, len(rideInfo.Stops))
	client.Cache.Set(key, rideInfo, rideTTL)
	return rideInfo.fill(leg), nil
}

// ride is what Transitous adds to a leg.
type ride struct {
	Stops          []journey.Stop
	Line, Operator string
}

// fill returns the leg with the ride's stop list; line and operator are
// only filled in where the leg has none.
func (rideInfo ride) fill(leg journey.Leg) journey.Leg {
	leg.Stops = rideInfo.Stops
	if leg.Line == "" {
		leg.Line = rideInfo.Line
	}
	if leg.Operator == "" {
		leg.Operator = rideInfo.Operator
	}
	return leg
}

// findRide picks the leg's ride out of a search result: the direct ride
// with the leg's planned departure and arrival times.
func findRide(resp planResponse, leg journey.Leg) (ride, bool) {
	for _, itinerary := range resp.Itineraries {
		var rides []planLeg
		for _, plannerLeg := range itinerary.Legs {
			if plannerLeg.isRide() {
				rides = append(rides, plannerLeg)
			}
		}
		if len(rides) != 1 {
			continue
		}
		plannerLeg := rides[0]
		if plannerLeg.ScheduledStart.Equal(leg.Departure) && plannerLeg.ScheduledEnd.Equal(leg.Arrival) {
			return ride{Stops: stops(plannerLeg), Line: plannerLeg.RouteShortName, Operator: plannerLeg.AgencyName}, true
		}
	}
	return ride{}, false
}

// Route returns the first connection between two stops at or after the
// given time that uses local transport only.
func (client *Client) Route(ctx context.Context, from, to journey.Stop, when time.Time) (journey.Journey, error) {
	if err := needCoords(from, to); err != nil {
		return journey.Journey{}, err
	}
	progress.Logf(ctx, "Transitous: Verbindung %s → %s suchen", from.Name, to.Name)
	resp, err := client.plan(ctx, from, to, when, nil)
	if err != nil {
		return journey.Journey{}, err
	}
	for _, itinerary := range resp.Itineraries {
		if connection, ok := toJourney(itinerary.Legs, from, to); ok && !hasLongDistance(connection) {
			return connection, nil
		}
	}
	return journey.Journey{}, fmt.Errorf("transitous: no local connection found between %q and %q", from.Name, to.Name)
}

// Connection returns the connection between two stops that departs at the
// given time, or else the first one after it. Unlike Route it may use
// long-distance trains: it stands for a connection the user picked.
func (client *Client) Connection(ctx context.Context, from, to journey.Stop, departure time.Time) (journey.Journey, error) {
	if err := needCoords(from, to); err != nil {
		return journey.Journey{}, err
	}
	progress.Logf(ctx, "Transitous: Verbindung %s → %s ab %s suchen", from.Name, to.Name, departure.Format("02.01. 15:04"))
	resp, err := client.plan(ctx, from, to, departure.Add(-searchLead), nil)
	if err != nil {
		return journey.Journey{}, err
	}
	var later *journey.Journey
	for _, itinerary := range resp.Itineraries {
		connection, ok := toJourney(itinerary.Legs, from, to)
		if !ok {
			continue
		}
		start := connection.Legs[0].Departure
		if start.Equal(departure) {
			progress.Logf(ctx, "Transitous: Verbindung gefunden, %d Fahrt(en)", len(connection.Legs))
			return connection, nil
		}
		if later == nil && start.After(departure) {
			later = &connection
		}
	}
	if later != nil {
		progress.Logf(ctx, "Transitous: keine Abfahrt um %s, nächste Verbindung ab %s genommen",
			departure.Format("15:04"), later.Legs[0].Departure.In(departure.Location()).Format("15:04"))
		return *later, nil
	}
	return journey.Journey{}, fmt.Errorf("transitous: no connection found from %q to %q at %s", from.Name, to.Name, departure.Format("2006-01-02 15:04"))
}

func needCoords(from, to journey.Stop) error {
	if !from.HasCoord() || !to.HasCoord() {
		return fmt.Errorf("transitous: stop positions unknown for %q - %q", from.Name, to.Name)
	}
	return nil
}

// toJourney converts the rides of an itinerary; walks are dropped.
func toJourney(legs []planLeg, from, to journey.Stop) (journey.Journey, bool) {
	connection := journey.Journey{Origin: from.Name, Destination: to.Name}
	for _, plannerLeg := range legs {
		if !plannerLeg.isRide() {
			continue
		}
		leg := journey.Leg{
			Index:     len(connection.Legs),
			Product:   products[plannerLeg.Mode],
			Line:      plannerLeg.RouteShortName,
			Number:    strings.TrimLeft(plannerLeg.TripShortName, "0"),
			Operator:  plannerLeg.AgencyName,
			Departure: plannerLeg.ScheduledStart,
			Arrival:   plannerLeg.ScheduledEnd,
			Stops:     stops(plannerLeg),
		}
		// Long-distance lines come as a bare number ("11"), which reads
		// like a bus line.
		if leg.LongDistance() && strings.Trim(leg.Line, "0123456789") == "" {
			leg.Line = strings.TrimSpace("Fernzug " + leg.Line)
		}
		leg.Origin, leg.Destination = leg.Stops[0], leg.Stops[len(leg.Stops)-1]
		connection.Legs = append(connection.Legs, leg)
	}
	return connection, len(connection.Legs) > 0
}

func hasLongDistance(connection journey.Journey) bool {
	return slices.ContainsFunc(connection.Legs, journey.Leg.LongDistance)
}

// FindStop resolves a stop name to the best matching stop.
func (client *Client) FindStop(ctx context.Context, name string) (journey.Stop, error) {
	key := "stop|" + strings.ToLower(strings.TrimSpace(name))
	var hit journey.Stop
	if client.Cache.Get(key, &hit) && hit.HasCoord() {
		return hit, nil
	}
	progress.Logf(ctx, "Transitous: Haltestelle %q suchen", name)
	var matches []struct {
		Type string  `json:"type"`
		Name string  `json:"name"`
		ID   string  `json:"id"`
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
	}
	query := url.Values{"text": {name}}
	if err := client.HTTP.GetJSON(ctx, client.BaseURL+"/v1/geocode?"+query.Encode(), &matches); err != nil {
		return journey.Stop{}, fmt.Errorf("transitous: look up stop %q: %w", name, err)
	}
	for _, match := range matches {
		if match.Type == "STOP" && (match.Lat != 0 || match.Lon != 0) {
			stop := journey.Stop{Name: match.Name, ID: stationID(match.ID), Lat: match.Lat, Lon: match.Lon}
			client.Cache.Set(key, stop, stopTTL)
			return stop, nil
		}
	}
	return journey.Stop{}, fmt.Errorf("stop %q not found", name)
}

func stops(plannerLeg planLeg) []journey.Stop {
	places := append(append([]place{plannerLeg.From}, plannerLeg.IntermediateStops...), plannerLeg.To)
	out := make([]journey.Stop, len(places))
	for index, place := range places {
		out[index] = journey.Stop{
			Name: place.Name, ID: stationID(place.StopID), Lat: place.Lat, Lon: place.Lon,
			Arrival: place.ScheduledArrival, Departure: place.ScheduledDeparture,
		}
	}
	return out
}

// stationID turns a Transitous stop id into the nationwide station id
// (DHID): "de-DELFI_de:08416:1005:3:2" names platform 3:2 of station
// "de:08416:1005", as does "de:08416:1005_G". Ids of other shapes are
// returned without the feed prefix.
func stationID(id string) string {
	if _, rest, ok := strings.Cut(id, "_"); ok {
		id = rest
	}
	parts := strings.Split(id, ":")
	if len(parts) < 3 || parts[0] != "de" {
		return id
	}
	// Some feeds mark a station as "de:08335:6554_G".
	number, _, _ := strings.Cut(parts[2], "_")
	return strings.Join([]string{parts[0], parts[1], number}, ":")
}
