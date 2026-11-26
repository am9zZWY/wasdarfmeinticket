package efa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/httpx"
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

func stop(name string, lat, lon float64) journey.Stop {
	return journey.Stop{Name: name, Lat: lat, Lon: lon}
}

func ride(from, to journey.Stop, number string, depH, depM, arrH, arrM int) journey.Leg {
	return journey.Leg{
		Product: journey.ProductRegional, Number: number, Origin: from, Destination: to,
		Departure: time.Date(2026, 11, 24, depH, depM, 0, 0, berlin),
		Arrival:   time.Date(2026, 11, 24, arrH, arrM, 0, 0, berlin),
	}
}

var (
	tuebingen  = stop("Tübingen Hbf", 48.515645, 9.055392)
	reutlingen = stop("Reutlingen Hbf", 48.496076, 9.209116)
	stuttgart  = stop("Stuttgart Hbf", 48.784782, 9.182760)
	konstanz   = stop("Konstanz", 47.658740, 9.177333)
	radolfzell = stop("Radolfzell", 47.735810, 8.969247)
	nuernberg  = stop("Nürnberg Hbf", 49.445615, 11.082989)
	erlangen   = stop("Erlangen", 49.595900, 11.001900)
)

// testEngine serves one recorded trip response and a stop finder that
// answers every name with two candidates: a far one with a better match
// quality and the right one next to the requested position.
func testEngine(t *testing.T, fixture string, all []journey.Stop) *Engine {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/XML_STOPFINDER_REQUEST", func(writer http.ResponseWriter, request *http.Request) {
		name := request.URL.Query().Get("name_sf")
		locs := []map[string]any{{"id": "far", "name": name + " (elsewhere)", "coord": []float64{52.5, 13.4}, "matchQuality": 1000}}
		for _, stop := range all {
			if stop.Name == name {
				locs = append(locs, map[string]any{"id": "id:" + name, "name": name, "coord": []float64{stop.Lat + 0.001, stop.Lon}, "matchQuality": 500})
			}
		}
		json.NewEncoder(writer).Encode(map[string]any{"locations": locs})
	})
	mux.HandleFunc("/XML_TRIP_REQUEST2", func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("name_origin"); got == "far" || got == "" {
			t.Errorf("trip request used origin %q", got)
		}
		data, err := os.ReadFile("testdata/" + fixture)
		if err != nil {
			t.Error(err)
		}
		writer.Write(data)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 0, time.Millisecond
	return &Engine{
		Planners: []Planner{{Name: "test", BaseURL: srv.URL + "/", Nets: []string{"tub"}, BBox: BBox{MinLat: 47, MaxLat: 51, MinLon: 7, MaxLon: 14}}},
		HTTP:     httpClient,
		Location: berlin,
	}
}

func hasProduct(assessment tariff.Assessment, name string) bool {
	return slices.ContainsFunc(assessment.Products, func(product tariff.Product) bool { return product.Name == name })
}

func TestAssess(t *testing.T) {
	all := []journey.Stop{tuebingen, reutlingen, stuttgart, konstanz, radolfzell, nuernberg, erlangen}
	tests := []struct {
		name     string
		fixture  string
		leg      journey.Leg
		matched  bool
		zones    []tariff.ZoneSet
		products []string
	}{
		{
			name: "inside naldo", fixture: "trip_naldo.json",
			leg:      ride(tuebingen, reutlingen, "19382", 9, 58, 10, 8),
			matched:  true,
			zones:    []tariff.ZoneSet{{Net: "tub", Zones: []string{"11", "220"}}},
			products: []string{"Deutschlandticket (persönlich)", "Monatskarte (übertragbar)"},
		},
		{
			name: "naldo to VVS has no zones", fixture: "trip_bw_border.json",
			leg:      ride(tuebingen, stuttgart, "19382", 9, 58, 10, 56),
			matched:  true,
			products: []string{"Deutschland-Ticket", "Baden-Württemberg-Ticket, 2. Klasse, 1 Reisender"},
		},
		{
			name: "inside VHB", fixture: "trip_vhb.json",
			leg:     ride(konstanz, radolfzell, "87672", 9, 53, 10, 14),
			matched: true,
			zones:   []tariff.ZoneSet{{Net: "vhb", Zones: []string{"5", "4"}}},
		},
		{
			name: "VGN with trailing footpath and united zones", fixture: "trip_vgn.json",
			leg:     ride(nuernberg, erlangen, "39134", 9, 41, 10, 7),
			matched: true,
			zones: []tariff.ZoneSet{{Net: "vgn", Zones: []string{"100", "200", "397", "398", "400"},
				ZonesUnited: []string{"100", "200", "300", "400"}}},
			products: []string{"Bayern Ticket Tag"},
		},
		{
			name: "other train at the same time is not matched", fixture: "trip_naldo.json",
			leg: ride(tuebingen, reutlingen, "11111", 9, 58, 10, 8),
		},
		{
			name: "same train number at another time is not matched", fixture: "trip_naldo.json",
			leg: ride(tuebingen, reutlingen, "19382", 7, 58, 8, 8),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assessment := testEngine(t, testCase.fixture, all).Assess(context.Background(), testCase.leg)
			if assessment.Matched != testCase.matched {
				t.Fatalf("matched = %v (%s), want %v", assessment.Matched, assessment.Note, testCase.matched)
			}
			if !testCase.matched {
				if assessment.Note == "" || len(assessment.Zones) != 0 || len(assessment.Products) != 0 {
					t.Errorf("unmatched assessment carries data or no note: %+v", assessment)
				}
				return
			}
			if len(assessment.Zones) != len(testCase.zones) {
				t.Fatalf("zones = %+v, want %+v", assessment.Zones, testCase.zones)
			}
			for index, zoneSet := range testCase.zones {
				got := assessment.Zones[index]
				if got.Net != zoneSet.Net || !slices.Equal(got.Zones, zoneSet.Zones) || !slices.Equal(got.ZonesUnited, zoneSet.ZonesUnited) {
					t.Errorf("zones[%d] = %+v, want %+v", index, got, zoneSet)
				}
			}
			for _, productName := range testCase.products {
				if !hasProduct(assessment, productName) {
					t.Errorf("product %q missing", productName)
				}
			}
			if assessment.Source != "test" || !slices.Equal(assessment.KnownNets, []string{"tub"}) {
				t.Errorf("source/nets = %q %v", assessment.Source, assessment.KnownNets)
			}
		})
	}
}

// Two overlapping planners: one knows the train but has no zones for it,
// the other carries the naldo zones. Both answers must be merged.
func TestAssessMergesOverlappingPlanners(t *testing.T) {
	all := []journey.Stop{tuebingen, reutlingen}
	noZones := testEngine(t, "trip_bw_border.json", all).Planners[0]
	noZones.Name, noZones.Nets = "other", []string{"vgn"}
	withZones := testEngine(t, "trip_naldo.json", all).Planners[0]
	withZones.Name = "bw"
	failing := Planner{Name: "down", BaseURL: "http://127.0.0.1:1/", Nets: []string{"mvv"}, BBox: noZones.BBox}

	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 0, time.Millisecond
	engine := &Engine{Planners: []Planner{noZones, failing, withZones}, HTTP: httpClient, Location: berlin}
	// The border fixture's ride ends in Stuttgart; use the shared train and
	// departure so both planners match.
	assessment := engine.Assess(context.Background(), ride(tuebingen, reutlingen, "19382", 9, 58, 10, 8))
	if !assessment.Matched || assessment.Source != "other + bw" {
		t.Fatalf("assessment = %+v", assessment)
	}
	if len(assessment.Zones) != 1 || assessment.Zones[0].Net != "tub" {
		t.Errorf("zones = %+v", assessment.Zones)
	}
	if !slices.Equal(assessment.KnownNets, []string{"vgn", "tub"}) {
		t.Errorf("known nets = %v (a failed planner must not contribute)", assessment.KnownNets)
	}
	if !hasProduct(assessment, "Deutschland-Ticket") || !hasProduct(assessment, "Monatskarte (übertragbar)") {
		t.Errorf("products not merged: %+v", assessment.Products)
	}
	if !strings.Contains(assessment.Note, "down:") {
		t.Errorf("note = %q, want the failed planner mentioned", assessment.Note)
	}
}

// twinResponse lists one ride twice, the way EFA-BW does for the Gäubahn
// ICs: as the regional train with local fares, and as the IC without.
const twinResponse = `{"journeys":[
 {"legs":[{"origin":{"departureTimePlanned":"2026-11-24T08:14:00Z"},"destination":{"arrivalTimePlanned":"2026-11-24T09:26:00Z"},
   "transportation":{"number":"RE 87","product":{"class":13,"name":"R-Bahn"},"properties":{"trainType":"RE","trainNumber":"50183"}}}],
  "fare":{"tickets":[{"name":"Deutschland-Ticket","net":"NVBW","fromLeg":0,"toLeg":0}]}},
 {"legs":[{"origin":{"departureTimePlanned":"2026-11-24T08:14:00Z"},"destination":{"arrivalTimePlanned":"2026-11-24T09:26:00Z"},
   "transportation":{"number":"183","product":{"class":15,"name":"Zug"},"properties":{"trainType":"IC","trainNumber":"183"}}}],
  "fare":{}},
 {"legs":[{"origin":{"departureTimePlanned":"2026-11-24T08:58:00Z"},"destination":{"arrivalTimePlanned":"2026-11-24T09:54:00Z"},
   "transportation":{"number":"117","product":{"class":16,"name":"Zug"},"properties":{"trainType":"ICE","trainNumber":"117"}}}],
  "fare":{}}]}`

func TestExtractLocalTwin(t *testing.T) {
	var resp tripResponse
	if err := json.Unmarshal([]byte(twinResponse), &resp); err != nil {
		t.Fatal(err)
	}
	intercity := ride(stop("Horb", 48.44, 8.69), stop("Singen", 47.76, 8.84), "183", 9, 14, 10, 26)
	intercity.Product = journey.ProductECIC

	// bahn.de says IC 183: the regional twin with its fares is used.
	assessment, ok := extract(resp, intercity)
	if !ok || assessment.LocalLine != "RE 87" || !hasProduct(assessment, "Deutschland-Ticket") {
		t.Errorf("IC with twin = %+v, %v", assessment, ok)
	}
	// A twin must share both times, not just the departure.
	late := intercity
	late.Arrival = late.Arrival.Add(5 * time.Minute)
	if assessment, ok := extract(resp, late); !ok || assessment.LocalLine != "" || len(assessment.Products) != 0 {
		t.Errorf("IC with other arrival = %+v, %v", assessment, ok)
	}
	// A real ICE has no twin and stays long-distance.
	ice := ride(stop("Stuttgart", 48.78, 9.18), stop("Ulm", 48.40, 9.98), "117", 9, 58, 10, 54)
	ice.Product = journey.ProductICE
	if assessment, ok := extract(resp, ice); !ok || assessment.LocalLine != "" {
		t.Errorf("ICE = %+v, %v", assessment, ok)
	}
	// bahn.de already says RE (number 50183): plain match, named as local.
	regional := ride(intercity.Origin, intercity.Destination, "50183", 9, 14, 10, 26)
	if assessment, ok := extract(resp, regional); !ok || assessment.LocalLine != "RE 87" {
		t.Errorf("RE = %+v, %v", assessment, ok)
	}
	// A regional leg never borrows another train's fares just by its times.
	other := ride(intercity.Origin, intercity.Destination, "99999", 9, 14, 10, 26)
	if _, ok := extract(resp, other); ok {
		t.Error("regional leg with another number matched by times")
	}
}

func TestAssessUsesStationIDs(t *testing.T) {
	var origin, destination string
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/XML_TRIP_REQUEST2" {
			t.Errorf("unexpected request %s: stops with a station id need no stop search", request.URL.Path)
		}
		origin, destination = request.URL.Query().Get("name_origin"), request.URL.Query().Get("name_destination")
		http.ServeFile(writer, request, "testdata/trip_naldo.json")
	}))
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 0, time.Millisecond
	engine := &Engine{Planners: []Planner{{Name: "test", BaseURL: srv.URL + "/", BBox: BBox{MinLat: 47, MaxLat: 51, MinLon: 7, MaxLon: 14}}},
		HTTP: httpClient, Location: berlin}
	from, to := tuebingen, reutlingen
	from.ID, to.ID = "de:08416:11000", "de:08415:22006"
	assessment := engine.Assess(context.Background(), ride(from, to, "19382", 9, 58, 10, 8))
	if !assessment.Matched || origin != "de:08416:11000" || destination != "de:08415:22006" {
		t.Errorf("assessment = %+v, asked %q -> %q", assessment, origin, destination)
	}
}

// A planner answering "too many requests" is left alone for a while, and
// the other planners still answer.
func TestRateLimitedPlannerIsPaused(t *testing.T) {
	limited := 0
	busy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		limited++
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(busy.Close)
	engine := testEngine(t, "trip_naldo.json", []journey.Stop{tuebingen, reutlingen})
	engine.Planners = []Planner{{Name: "busy", BaseURL: busy.URL + "/", BBox: engine.Planners[0].BBox}, engine.Planners[0]}
	leg := ride(tuebingen, reutlingen, "19382", 9, 58, 10, 8)
	for index := 0; index < 3; index++ {
		if assessment := engine.Assess(context.Background(), leg); !assessment.Matched || assessment.Source != "test" || !strings.Contains(assessment.Note, "busy:") {
			t.Fatalf("assessment %d = %+v", index, assessment)
		}
	}
	if limited != 1 {
		t.Errorf("rate-limited planner was asked %d times, want once", limited)
	}
}

// The same line between the same stops at another time of day is answered
// from the cache; another line or a leg without a line name is not.
func TestAssessCache(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		http.ServeFile(writer, request, "testdata/trip_naldo.json")
	}))
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 0, time.Millisecond
	engine := &Engine{Planners: []Planner{{Name: "test", BaseURL: srv.URL + "/", Nets: []string{"tub"}, BBox: BBox{MinLat: 47, MaxLat: 51, MinLon: 7, MaxLon: 14}}},
		HTTP: httpClient, Location: berlin, Cache: cache.Open("")}
	from, to := tuebingen, reutlingen
	from.ID, to.ID = "de:08416:11000", "de:08415:22006"

	morning := ride(from, to, "19382", 9, 58, 10, 8)
	morning.Line = "RE6"
	first := engine.Assess(context.Background(), morning)
	if !first.Matched || requests != 1 {
		t.Fatalf("first = %+v, requests = %d", first, requests)
	}
	// Two hours later: another train number and time, which the recorded
	// answer does not contain. Only the cache can answer this.
	later := ride(from, to, "19390", 11, 58, 12, 8)
	later.Line = "RE6"
	second := engine.Assess(context.Background(), later)
	if !second.Matched || requests != 1 || len(second.Zones) != 1 || second.Zones[0].Net != "tub" || second.Source != "test" {
		t.Errorf("second = %+v, requests = %d", second, requests)
	}
	other := later
	other.Line = "RB63"
	if assessment := engine.Assess(context.Background(), other); assessment.Matched || requests != 2 {
		t.Errorf("another line must be asked: %+v, requests = %d", assessment, requests)
	}
	unnamed := later
	unnamed.Line = ""
	if assessment := engine.Assess(context.Background(), unnamed); assessment.Matched || requests != 3 {
		t.Errorf("a leg without line name must be asked: %+v, requests = %d", assessment, requests)
	}
}

func TestAssessOutsideCoverage(t *testing.T) {
	engine := testEngine(t, "trip_naldo.json", nil)
	hamburg := stop("Hamburg Hbf", 53.55, 10.0)
	assessment := engine.Assess(context.Background(), ride(tuebingen, hamburg, "1", 9, 0, 15, 0))
	if assessment.Matched || assessment.Note == "" {
		t.Errorf("assessment = %+v", assessment)
	}
	assessment = engine.Assess(context.Background(), ride(journey.Stop{Name: "x"}, hamburg, "1", 9, 0, 15, 0))
	if assessment.Matched || assessment.Note == "" {
		t.Errorf("assessment without coordinates = %+v", assessment)
	}
}

func TestDefaultPlanners(t *testing.T) {
	planners := DefaultPlanners()
	if len(planners) == 0 {
		t.Fatalf("planners = %d", len(planners))
	}
	for _, planner := range planners {
		if planner.Name == "" || planner.BaseURL == "" || len(planner.Nets) == 0 || planner.BBox.MinLat >= planner.BBox.MaxLat {
			t.Errorf("incomplete planner: %+v", planner)
		}
	}
}
