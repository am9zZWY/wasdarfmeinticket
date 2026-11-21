package transitous

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/httpx"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
)

// The fixture is a recorded answer for Rottenburg -> Konstanz on Mon
// 2026-11-23: RB74 09:45Z, IC87 (IC 185 under its local name) 10:14Z, S6 11:32Z.
func testClient(t *testing.T, check func(request *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/geocode" {
			// Shape recorded from Transitous: places and stops mixed.
			if request.URL.Query().Get("text") == "Rottenburg(Neckar)" {
				writer.Write([]byte(`[{"type":"PLACE","name":"Rottenburg am Neckar","id":"node/240090562","lat":48.47696,"lon":8.9336788},
					{"type":"STOP","name":"Rottenburg (Neckar)","id":"de-amarillo-bw_de:08416:1005","lat":48.472434,"lon":8.935857}]`))
			} else {
				writer.Write([]byte(`[{"type":"PLACE","name":"Somewhere","id":"node/1","lat":1,"lon":1}]`))
			}
			return
		}
		if request.URL.Path != "/v5/plan" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		if check != nil {
			check(request)
		}
		http.ServeFile(writer, request, "testdata/plan.json")
	}))
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 0, time.Millisecond
	httpClient.UserAgent = DefaultUserAgent
	return &Client{BaseURL: srv.URL, HTTP: httpClient}
}

func utc(hour, min int) time.Time { return time.Date(2026, 11, 23, hour, min, 0, 0, time.UTC) }

var (
	rottenburg = journey.Stop{Name: "Rottenburg(Neckar)", ID: "8005197", Lat: 48.472434, Lon: 8.935547}
	horb       = journey.Stop{Name: "Horb", ID: "8000177", Lat: 48.441475, Lon: 8.688982}
	singen     = journey.Stop{Name: "Singen(Hohentwiel)", ID: "8000073", Lat: 47.758879, Lon: 8.841268}
	konstanz   = journey.Stop{Name: "Konstanz", ID: "8003400", Lat: 47.658757, Lon: 9.177312}
)

func TestRoute(t *testing.T) {
	client := testClient(t, func(request *http.Request) {
		query := request.URL.Query()
		if query.Get("fromPlace") != "48.472434,8.935547" || query.Get("toPlace") != "47.658757,9.177312" || query.Get("time") != "2026-11-23T09:40:00Z" ||
			query.Get("maxPreTransitTime") != "300" || query.Get("maxPostTransitTime") != "300" {
			t.Errorf("query = %v", query)
		}
		if userAgent := request.Header.Get("User-Agent"); !strings.HasPrefix(userAgent, "wasdarfmeinticket/") || !strings.Contains(userAgent, "@") {
			t.Errorf("User-Agent %q lacks name or contact", userAgent)
		}
	})
	connection, err := client.Route(context.Background(), rottenburg, konstanz, utc(9, 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(connection.Legs) != 3 || connection.Origin != "Rottenburg(Neckar)" || connection.Destination != "Konstanz" {
		t.Fatalf("journey = %+v", connection)
	}
	want := []struct {
		product, line, number, fromID, toID string
		dep                                 time.Time
		stops                               int
	}{
		{journey.ProductRegional, "RB74", "32503", "de:08416:1005", "de:08237:5001", utc(9, 45), 6},
		{journey.ProductRegional, "IC87", "50185", "de:08237:5001", "de:08335:6588", utc(10, 14), 4},
		{journey.ProductSBahn, "S6", "87679", "de:08335:6588", "", utc(11, 32), 12},
	}
	for index, expected := range want {
		leg := connection.Legs[index]
		if leg.Index != index || leg.Walk || leg.Product != expected.product || leg.Line != expected.line || leg.Number != expected.number ||
			leg.Origin.ID != expected.fromID || !leg.Departure.Equal(expected.dep) || len(leg.Stops) != expected.stops {
			t.Errorf("leg %d = %+v", index, leg)
		}
		if expected.toID != "" && leg.Destination.ID != expected.toID {
			t.Errorf("leg %d destination id = %q", index, leg.Destination.ID)
		}
	}
	// Intermediate stops carry times, so partial rides can be formed.
	regional := connection.Legs[0]
	if stop := regional.Stops[1]; stop.Name != "Bad Niedernau Bahnhof" || stop.ID != "de:08416:2505" || !stop.Arrival.Equal(utc(9, 48)) || !stop.Departure.Equal(utc(9, 49)) {
		t.Errorf("stop = %+v", stop)
	}
	if sub, ok := regional.Sub(0, 1); !ok || !sub.Arrival.Equal(utc(9, 48)) {
		t.Errorf("sub leg = %+v, %v", sub, ok)
	}
}

func TestEnrich(t *testing.T) {
	client := testClient(t, nil)
	// bahn.de calls this train IC 185; times identify it.
	intercity := journey.Leg{Index: 1, Product: journey.ProductECIC, Line: "IC 185", Number: "185",
		Origin: horb, Destination: singen, Departure: utc(10, 14), Arrival: utc(11, 26)}

	// The request asks for direct rides, starting a little before departure.
	asked := false
	checkingClient := testClient(t, func(request *http.Request) {
		asked = true
		if query := request.URL.Query(); query.Get("maxTransfers") != "0" || query.Get("time") != "2026-11-23T10:04:00Z" || query.Get("fromPlace") != "48.441475,8.688982" {
			t.Errorf("query = %v", query)
		}
	})
	// The fixture only has journeys with changes, so nothing matches here.
	if _, err := checkingClient.Enrich(context.Background(), intercity); err == nil || !asked {
		t.Errorf("enrich against whole journeys: err = %v, asked = %v", err, asked)
	}

	// The fixture holds whole journeys; cut it down to what a direct search returns.
	got, err := enrichFromFixture(t, client, intercity)
	if err != nil {
		t.Fatal(err)
	}
	if got.Line != "IC 185" || got.Product != journey.ProductECIC || got.Origin.ID != "8000177" {
		t.Errorf("bahn.de's own data must be kept: %+v", got)
	}
	names := make([]string, len(got.Stops))
	for index, stop := range got.Stops {
		names[index] = stop.Name
	}
	if strings.Join(names, ", ") != "Horb Bahnhof/ZOB, Rottweil Bahnhof, Tuttlingen Bahnhof, Singen (Htw) Bahnhof" {
		t.Errorf("stops = %v", names)
	}
	if got.Operator != "DB Fernverkehr (Codesharing)" {
		t.Errorf("operator = %q", got.Operator)
	}

	other := intercity
	other.Arrival = other.Arrival.Add(time.Minute)
	if _, err := enrichFromFixture(t, client, other); err == nil {
		t.Error("ride with another arrival time: expected error")
	}
	if _, err := client.Enrich(context.Background(), journey.Leg{Walk: true}); err == nil {
		t.Error("walk: expected error")
	}
	if _, err := client.Enrich(context.Background(), journey.Leg{Origin: journey.Stop{Name: "x"}, Destination: horb}); err == nil {
		t.Error("leg without position: expected error")
	}
}

// enrichFromFixture runs Enrich against a server that returns each ride of
// the recorded journey as its own direct itinerary.
func enrichFromFixture(t *testing.T, base *Client, leg journey.Leg) (journey.Leg, error) {
	t.Helper()
	full, err := base.plan(context.Background(), rottenburg, konstanz, utc(9, 40), nil)
	if err != nil {
		t.Fatal(err)
	}
	var direct planResponse
	for _, plannerLeg := range full.Itineraries[0].Legs {
		if plannerLeg.isRide() {
			direct.Itineraries = append(direct.Itineraries, struct {
				Legs []planLeg `json:"legs"`
			}{Legs: []planLeg{{Mode: "WALK"}, plannerLeg}})
		}
	}
	rideInfo, ok := findRide(direct, leg)
	if !ok {
		return leg, errors.New("ride not found")
	}
	return rideInfo.fill(leg), nil
}

func TestConnection(t *testing.T) {
	client := testClient(t, nil)
	// The departure the user picked: the journey via the IC, which
	// Transitous lists under its local name and number.
	connection, err := client.Connection(context.Background(), rottenburg, konstanz, utc(9, 45))
	if err != nil {
		t.Fatal(err)
	}
	if len(connection.Legs) != 3 || !connection.Legs[0].Departure.Equal(utc(9, 45)) || connection.Legs[1].Line != "IC87" || connection.Legs[1].Number != "50185" {
		t.Fatalf("journey = %+v", connection)
	}
	// No departure at that minute: the next one is taken.
	connection, err = client.Connection(context.Background(), rottenburg, konstanz, utc(10, 0))
	if err != nil || !connection.Legs[0].Departure.Equal(utc(10, 39)) {
		t.Errorf("next connection = %+v, %v", connection.Legs, err)
	}
	if _, err := client.Connection(context.Background(), rottenburg, konstanz, utc(23, 0)); err == nil {
		t.Error("nothing after the given time: expected error")
	}
}

func TestLongDistanceLineName(t *testing.T) {
	place := func(name string) place { return place{Name: name, StopID: "de-DELFI_de:1:2", Lat: 48, Lon: 9} }
	legs := []planLeg{
		{Mode: "HIGHSPEED_RAIL", TripID: "t1", RouteShortName: "11", From: place("Ulm"), To: place("München")},
		{Mode: "BUS", TripID: "t2", RouteShortName: "62", From: place("A"), To: place("B")},
		{Mode: "LONG_DISTANCE", TripID: "t3", RouteShortName: "IC 185", From: place("C"), To: place("D")},
	}
	connection, ok := toJourney(legs, journey.Stop{Name: "Ulm"}, journey.Stop{Name: "D"})
	if !ok || connection.Legs[0].Line != "Fernzug 11" || connection.Legs[1].Line != "62" || connection.Legs[2].Line != "IC 185" {
		t.Errorf("lines = %q, %q, %q", connection.Legs[0].Line, connection.Legs[1].Line, connection.Legs[2].Line)
	}
}

func TestFindStop(t *testing.T) {
	client := testClient(t, nil)
	stop, err := client.FindStop(context.Background(), "Rottenburg(Neckar)")
	if err != nil || stop.Name != "Rottenburg (Neckar)" || stop.ID != "de:08416:1005" || stop.Lat != 48.472434 {
		t.Errorf("stop = %+v, %v", stop, err)
	}
	if _, err := client.FindStop(context.Background(), "nowhere"); err == nil {
		t.Error("no stop in results: expected error")
	}
}

func TestStationID(t *testing.T) {
	for input, want := range map[string]string{
		"de-DELFI_de:08416:1005:3:2": "de:08416:1005",
		"de-DELFI_de:08416:2505":     "de:08416:2505",
		"de-DELFI_de:08416:1005_G":   "de:08416:1005",
		"ch-opentransport_8503000":   "8503000",
		"plain":                      "plain",
	} {
		if got := stationID(input); got != want {
			t.Errorf("stationID(%q) = %q, want %q", input, got, want)
		}
	}
}
