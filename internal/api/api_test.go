package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/check"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dbapi"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dblink"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

const link = "https://www.bahn.de/buchung/start?vbid=a30982d3-9bbf-492d-828c-43a46ce38546"

type fakeDB struct {
	connection journey.Journey
	err        error
}

func (fake fakeDB) Resolve(context.Context, dblink.Ref) (journey.Journey, error) {
	return fake.connection, fake.err
}

type fakeTariff struct{ calls *[]int }

var fakeTariffMu sync.Mutex // legs are assessed in parallel

func (fake fakeTariff) Assess(_ context.Context, leg journey.Leg) tariff.Assessment {
	fakeTariffMu.Lock()
	*fake.calls = append(*fake.calls, leg.Index)
	fakeTariffMu.Unlock()
	return tariff.Assessment{Matched: true, Source: "fake", KnownNets: []string{"tub"},
		Zones: []tariff.ZoneSet{{Net: "tub", Zones: []string{"11", "220"}}}}
}

// routeTariff answers like the BW planner does for naldo: only the zones at
// both ends of the ride, here derived from the stop names.
type routeTariff struct{}

func (routeTariff) Assess(_ context.Context, leg journey.Leg) tariff.Assessment {
	zone := map[string]string{"Tübingen Hbf": "11", "Rottenburg": "12", "Bieringen": "112", "Eyach": "17"}
	assessment := tariff.Assessment{Matched: true, Source: "fake", KnownNets: []string{"tub"}}
	from, okF := zone[leg.Origin.Name]
	to, okT := zone[leg.Destination.Name]
	if okF && okT {
		assessment.Zones = []tariff.ZoneSet{{Net: "tub", Zones: []string{from, to}}}
	}
	return assessment
}

// fakeTimetable knows one line: asked to enrich a leg, it supplies the
// stops between the leg's end stops; asked for a route, it returns the line.
type fakeTimetable struct{ connection journey.Journey }

func (fake fakeTimetable) Enrich(_ context.Context, leg journey.Leg) (journey.Leg, error) {
	all := fake.connection.Legs[0].Stops
	from, to := -1, -1
	for index, stop := range all {
		if stop.Name == leg.Origin.Name {
			from = index
		}
		if stop.Name == leg.Destination.Name {
			to = index
		}
	}
	if from < 0 || to <= from {
		return leg, errors.New("ride not found")
	}
	leg.Stops = all[from : to+1]
	return leg, nil
}

func (fake fakeTimetable) Route(_ context.Context, from, to journey.Stop, when time.Time) (journey.Journey, error) {
	if when.Hour() != 10 || when.Weekday() == time.Saturday || when.Weekday() == time.Sunday {
		return journey.Journey{}, errors.New("lookup must use a weekday morning")
	}
	if from.ID == "" || to.ID == "" {
		return journey.Journey{}, errors.New("stops must be resolved first")
	}
	return fake.connection, nil
}

func (fake fakeTimetable) Connection(_ context.Context, from, to journey.Stop, dep time.Time) (journey.Journey, error) {
	if !from.HasCoord() || !to.HasCoord() || dep.IsZero() {
		return journey.Journey{}, errors.New("connection needs positions and a time")
	}
	return fake.connection, nil
}

type fakeStops struct{}

func (fakeStops) FindStop(_ context.Context, name string) (journey.Stop, error) {
	if name == "Nirgendwo" {
		return journey.Stop{}, errors.New("stop not found")
	}
	return journey.Stop{Name: name, ID: "8000141", Lat: 48.5, Lon: 9.05}, nil
}

// withoutStops strips the stop lists, as a decoded share link has none.
func withoutStops(connection journey.Journey) journey.Journey {
	legs := make([]journey.Leg, len(connection.Legs))
	copy(legs, connection.Legs)
	for index := range legs {
		legs[index].Stops = nil
	}
	connection.Legs = legs
	return connection
}

// line builds one regional leg calling at the given stops every 5 minutes.
func line(names ...string) journey.Journey {
	start := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	leg := journey.Leg{Product: journey.ProductRegional, Number: "1", Departure: start,
		Arrival: start.Add(time.Duration(len(names)-1) * 5 * time.Minute)}
	for index, name := range names {
		when := start.Add(time.Duration(index) * 5 * time.Minute)
		leg.Stops = append(leg.Stops, journey.Stop{Name: name, Arrival: when, Departure: when})
	}
	leg.Origin, leg.Destination = leg.Stops[0], leg.Stops[len(names)-1]
	return journey.Journey{Origin: names[0], Destination: names[len(names)-1], Legs: []journey.Leg{leg}}
}

func TestTicketsByRoute(t *testing.T) {
	ticketRoute := line("Tübingen Hbf", "Rottenburg", "Bieringen", "Eyach")
	// The share link yields the ride without stops; the timetable adds them.
	checked := withoutStops(line("Rottenburg", "Bieringen", "Eyach"))
	service := &Service{DB: fakeDB{connection: checked}, Tariff: routeTariff{}, Timetable: fakeTimetable{ticketRoute}, Stops: fakeStops{}}

	// A ticket for Tübingen - Eyach must cover Rottenburg - Eyach, although
	// the planner names only the end zones of each ride. Described by stop
	// names and by a share link (the fake DB returns the checked ride for it).
	code, out := post(t, service, `{"link":"`+link+`","tickets":[
		{"type":"verbund","from":"Tübingen Hbf","to":"Eyach"},
		{"type":"verbund","link":"`+link+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, out["error"])
	}
	var results []check.Result
	if err := json.Unmarshal(out["results"], &results); err != nil {
		t.Fatal(err)
	}
	byName, byLink := results[0], results[1]
	if byName.Ticket.Net != "naldo" || strings.Join(byName.Ticket.Zones, ",") != "11,17,12,112" || byName.Ticket.From != "Tübingen Hbf" {
		t.Errorf("ticket by names = %+v", byName.Ticket)
	}
	if strings.Join(byLink.Ticket.Zones, ",") != "12,17,112" || byLink.Ticket.Link != link {
		t.Errorf("ticket by link = %+v", byLink.Ticket)
	}
	for index, result := range results {
		if result.Verdict != check.Valid {
			t.Errorf("results[%d] = %s (%s)", index, result.Verdict, result.Legs[0].Reason)
		}
	}

	code, out = post(t, service, `{"link":"`+link+`","tickets":[{"type":"verbund","from":"Tübingen Hbf","to":"Nirgendwo"}]}`)
	if code != http.StatusBadRequest || !strings.Contains(string(out["error"]), "tickets[0]") {
		t.Errorf("unknown stop: %d %s", code, out["error"])
	}

	// Resolve endpoint.
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tickets/resolve",
		strings.NewReader(`{"type":"verbund","from":"Tübingen Hbf","to":"Eyach"}`)))
	var ticket check.Ticket
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil || rec.Code != http.StatusOK ||
		ticket.Net != "naldo" || len(ticket.Zones) != 4 {
		t.Errorf("resolve: %d %s", rec.Code, rec.Body)
	}
	// Naming only the Verbund means the whole network, and the answer says so.
	code, out = post(t, service, `{"link":"`+link+`","tickets":[{"type":"verbund","net":"naldo"}]}`)
	if err := json.Unmarshal(out["results"], &results); err != nil || code != http.StatusOK ||
		!results[0].Ticket.Network || results[0].Verdict != check.Valid {
		t.Errorf("net only: %d %+v %s", code, results, out["error"])
	}

	// A route without any Verbund zones cannot become a ticket.
	none := &Service{Tariff: routeTariff{}, Timetable: fakeTimetable{line("Horb", "Sulz")}, Stops: fakeStops{}}
	if _, err := none.ResolveTicket(context.Background(), check.Ticket{Type: check.TypeVerbund, From: "Horb", To: "Sulz"}); err == nil {
		t.Error("route without zones: expected error")
	}
}

func testJourney() journey.Journey {
	dep := time.Date(2026, 11, 24, 9, 58, 0, 0, time.UTC)
	return journey.Journey{Origin: "Tübingen Hbf", Destination: "Hamburg Hbf", Legs: []journey.Leg{
		{Index: 0, Product: journey.ProductRegional, Departure: dep},
		{Index: 1, Walk: true},
		{Index: 2, Product: journey.ProductICE, Line: "ICE 682", Departure: dep},
	}}
}

func post(t *testing.T, service *Service, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/check", strings.NewReader(body)))
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body)
	}
	return rec.Code, out
}

func TestCheckEndpoint(t *testing.T) {
	var calls []int
	service := &Service{DB: fakeDB{connection: testJourney()}, Tariff: fakeTariff{&calls}}
	code, out := post(t, service, `{"link":"`+link+`","tickets":[
		{"type":"verbund","net":"naldo","zones":["11","220"]},
		{"type":"deutschlandticket"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, out["error"])
	}
	slices.Sort(calls)
	if !slices.Equal(calls, []int{0, 2}) {
		t.Errorf("tariff source asked for legs %v, want both trains but not the walk", calls)
	}
	var results []check.Result
	if err := json.Unmarshal(out["results"], &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	// Zone ticket: valid on the regional leg, never on the ICE, even though
	// the fake source reports matching zones for it.
	zone := results[0]
	if zone.Legs[0].Verdict != check.Valid || zone.Legs[1].Verdict != check.NotRequired ||
		zone.Legs[2].Verdict != check.Invalid || zone.Verdict != check.Partial {
		t.Errorf("zone ticket: %+v", zone)
	}
	// Deutschlandticket: valid on the regional leg, not on the ICE.
	if deutschlandResult := results[1]; deutschlandResult.Verdict != check.Partial || deutschlandResult.Legs[0].Verdict != check.Valid || deutschlandResult.Legs[2].Verdict != check.Invalid {
		t.Errorf("deutschlandticket: %+v", deutschlandResult)
	}
}

func TestCheckEndpointErrors(t *testing.T) {
	var calls []int
	ok := &Service{DB: fakeDB{connection: testJourney()}, Tariff: fakeTariff{&calls}}
	tests := []struct {
		name    string
		service *Service
		body    string
		want    int
	}{
		{"malformed JSON", ok, `{`, http.StatusBadRequest},
		{"unknown field", ok, `{"link":"` + link + `","tickets":[{"type":"deutschlandticket"}],"x":1}`, http.StatusBadRequest},
		{"no tickets", ok, `{"link":"` + link + `"}`, http.StatusBadRequest},
		{"bad ticket", ok, `{"link":"` + link + `","tickets":[{"type":"bahncard100"}]}`, http.StatusBadRequest},
		{"no link", ok, `{"link":"hello","tickets":[{"type":"deutschlandticket"}]}`, http.StatusBadRequest},
		{"expired link", &Service{DB: fakeDB{err: dbapi.ErrNotFound}, Tariff: fakeTariff{&calls}},
			`{"link":"` + link + `","tickets":[{"type":"deutschlandticket"}]}`, http.StatusNotFound},
		{"blocked by bahn.de", &Service{DB: fakeDB{err: fmt.Errorf("load: %w", dbapi.ErrBlocked)}, Tariff: fakeTariff{&calls}},
			`{"link":"` + link + `","tickets":[{"type":"deutschlandticket"}]}`, http.StatusServiceUnavailable},
		{"upstream failure", &Service{DB: fakeDB{err: errors.New("connection refused")}, Tariff: fakeTariff{&calls}},
			`{"link":"` + link + `","tickets":[{"type":"deutschlandticket"}]}`, http.StatusBadGateway},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			code, out := post(t, testCase.service, testCase.body)
			if code != testCase.want || len(out["error"]) == 0 {
				t.Errorf("status %d, body %v; want %d with error", code, out, testCase.want)
			}
		})
	}
}

// Partial validity needs intermediate stops, which only the timetable has.
func TestPartialNeedsTimetable(t *testing.T) {
	full := line("Rottenburg", "Bieringen", "Eyach", "Horb")
	body := `{"link":"` + link + `","tickets":[{"type":"verbund","net":"naldo"}]}`
	verdict := func(service *Service) check.LegResult {
		code, out := post(t, service, body)
		var results []check.Result
		if err := json.Unmarshal(out["results"], &results); err != nil || code != http.StatusOK {
			t.Fatalf("status %d: %s", code, out["error"])
		}
		return results[0].Legs[0]
	}
	with := verdict(&Service{DB: fakeDB{connection: withoutStops(full)}, Tariff: routeTariff{}, Timetable: fakeTimetable{full}})
	if with.Verdict != check.Partial || with.ValidTo != "Eyach" {
		t.Errorf("with timetable: %+v", with)
	}
	without := verdict(&Service{DB: fakeDB{connection: withoutStops(full)}, Tariff: routeTariff{}})
	if without.Verdict != check.Invalid || without.ValidTo != "" {
		t.Errorf("without timetable: %+v", without)
	}
	// A timetable that does not know the ride changes nothing either.
	unknown := verdict(&Service{DB: fakeDB{connection: withoutStops(full)}, Tariff: routeTariff{}, Timetable: fakeTimetable{line("X", "Y")}})
	if unknown.Verdict != check.Invalid {
		t.Errorf("ride unknown to the timetable: %+v", unknown)
	}
}

func TestMarkdown(t *testing.T) {
	var calls []int
	connection := testJourney()
	berlinDep := time.Date(2026, 11, 20, 15, 45, 0, 0, time.UTC) // 16:45 in Berlin, a Friday
	for index := range connection.Legs {
		connection.Legs[index].Departure, connection.Legs[index].Arrival = berlinDep, berlinDep.Add(25*time.Minute)
		connection.Legs[index].Origin.Name, connection.Legs[index].Destination.Name = "A", "B"
	}
	connection.Legs[0].Line = "RB74"
	service := &Service{DB: fakeDB{connection: connection}, Tariff: fakeTariff{&calls}}
	body := `{"link":"` + link + `","tickets":[{"type":"verbund","net":"naldo","zones":["11","220"]},
		{"type":"deutschlandticket"},{"type":"laenderticket","name":"bayern-ticket"}]}`

	get := func(target, accept, body string) (int, string, string) {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		service.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()
	}

	code, ctype, markdown := get("/v1/check", "text/markdown", body)
	if code != http.StatusOK || !strings.HasPrefix(ctype, "text/markdown") {
		t.Fatalf("status %d, content type %q", code, ctype)
	}
	for _, want := range []string{
		"# Tübingen Hbf → Hamburg Hbf\n",
		"Fr 20.11.2026, 16:45–17:10",
		"- **Verbundticket naldo, Zonen 11, 220**: teilweise gültig",
		"- **Deutschlandticket**: teilweise gültig",
		"- **Bayern-Ticket**: ",
		"1. RB74: A 16:45 → B 17:10",
		"   - Zonen: naldo 11, 220 (Tarifauskunft fake)",
		"2. Fußweg: A 16:45 → B 17:10",
		"**Deine Tickets decken die Verbindung nur teilweise ab.**",
		"   - ganze Fahrt: Verbundticket naldo, Zonen 11, 220; Deutschlandticket",
		"   - ganze Fahrt: **kein Ticket**",
		"   - nicht prüfbar: Bayern-Ticket",
		"## Noch ohne Ticket\n\n- ICE 682: A → B\n",
		"## Deutschlandticket: teilweise gültig",
		"- ICE 682 A → B: **nicht gültig**. Fernverkehr",
		"- Fußweg A → B: **kein Ticket nötig**.",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown lacks %q:\n%s", want, markdown)
		}
	}

	if code, ctype, _ := get("/v1/check?format=markdown", "", body); code != http.StatusOK || !strings.HasPrefix(ctype, "text/markdown") {
		t.Errorf("?format=markdown: %d %q", code, ctype)
	}
	if _, ctype, _ := get("/v1/check", "", body); !strings.HasPrefix(ctype, "application/json") {
		t.Errorf("default content type %q", ctype)
	}
	if _, ctype, _ := get("/v1/check?format=json", "text/markdown", body); !strings.HasPrefix(ctype, "application/json") {
		t.Errorf("?format=json must win over Accept: %q", ctype)
	}
	code, ctype, markdown = get("/v1/check", "text/plain", `{"link":"nope","tickets":[{"type":"deutschlandticket"}]}`)
	if code != http.StatusBadRequest || !strings.HasPrefix(ctype, "text/markdown") || !strings.HasPrefix(markdown, "Fehler: ") {
		t.Errorf("markdown error: %d %q %q", code, ctype, markdown)
	}
}

// Two Verbund tickets on one connection: the combined view shows which
// ticket covers which stretch and what is left.
func TestCoverageInResponse(t *testing.T) {
	full := line("Rottenburg", "Bieringen", "Eyach", "Horb")
	service := &Service{DB: fakeDB{connection: withoutStops(full)}, Tariff: routeTariff{}, Timetable: fakeTimetable{full}}
	code, out := post(t, service, `{"link":"`+link+`","tickets":[{"type":"verbund","net":"naldo"},{"type":"verbund","net":"vhb"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, out["error"])
	}
	var cov []check.LegCoverage
	if err := json.Unmarshal(out["coverage"], &cov); err != nil {
		t.Fatal(err)
	}
	if string(out["verdict"]) != `"partial"` || len(cov) != 1 || cov[0].Verdict != check.Partial || len(cov[0].Spans) != 2 {
		t.Fatalf("verdict %s, coverage %+v", out["verdict"], cov)
	}
	if span := cov[0].Spans[0]; span.From != "Rottenburg" || span.To != "Eyach" || len(span.Tickets) != 1 || span.Tickets[0] != 0 {
		t.Errorf("covered span = %+v", span)
	}
	if span := cov[0].Spans[1]; span.From != "Eyach" || span.To != "Horb" || len(span.Tickets) != 0 {
		t.Errorf("gap = %+v", span)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/check", strings.NewReader(
		`{"link":"`+link+`","tickets":[{"type":"verbund","net":"naldo"},{"type":"verbund","net":"vhb"}]}`))
	req.Header.Set("Accept", "text/markdown")
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	markdown := rec.Body.String()
	for _, want := range []string{
		"   - Rottenburg → Eyach: Verbundticket naldo, ganzes Netz\n",
		"   - Eyach → Horb: **kein Ticket**\n",
		"## Noch ohne Ticket\n\n- Zug 1: Eyach → Horb\n",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown lacks %q:\n%s", want, markdown)
		}
	}
	if strings.Index(markdown, "## Verbindung") > strings.Index(markdown, "## Tickets") {
		t.Error("the combined view must come before the per-ticket list")
	}
}

// A search link and from/to/departure need no lookup at bahn.de.
func TestConnectionWithoutShareLink(t *testing.T) {
	full := line("Rottenburg", "Bieringen", "Eyach")
	service := &Service{DB: fakeDB{err: errors.New("bahn.de must not be asked")}, Tariff: routeTariff{},
		Timetable: fakeTimetable{full}, Stops: fakeStops{}}
	const searchLink = "https://www.bahn.de/buchung/fahrplan/suche#sts=true&so=Rottenburg&zo=Eyach" +
		"&soid=A%3D1%40O%3DRottenburg%40X%3D8935547%40Y%3D48472434%40L%3D8005197%40" +
		"&zoid=A%3D1%40O%3DEyach%40X%3D8783099%40Y%3D48446284%40L%3D8001946%40&hd=2026-11-23T10:40:12&hza=D"
	for name, body := range map[string]string{
		"search link":       `{"link":"` + searchLink + `","tickets":[{"type":"verbund","net":"naldo"}]}`,
		"from/to/departure": `{"from":"Rottenburg","to":"Eyach","departure":"2026-11-23T10:40","tickets":[{"type":"verbund","net":"naldo"}]}`,
	} {
		code, out := post(t, service, body)
		if code != http.StatusOK {
			t.Errorf("%s: status %d: %s", name, code, out["error"])
			continue
		}
		if string(out["verdict"]) != `"valid"` || !strings.Contains(string(out["note"]), "erste Verbindung ab 10:40 Uhr") {
			t.Errorf("%s: verdict %s, note %s", name, out["verdict"], out["note"])
		}
	}
	// The note is part of the text output.
	req := httptest.NewRequest(http.MethodPost, "/v1/check", strings.NewReader(`{"link":"`+searchLink+`","tickets":[{"type":"deutschlandticket"}]}`))
	req.Header.Set("Accept", "text/markdown")
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "\nEs wurde die erste Verbindung ab 10:40 Uhr genommen (der Suchlink nennt keine bestimmte Verbindung).\n") {
		t.Errorf("markdown lacks the note:\n%s", rec.Body.String())
	}

	for name, body := range map[string]string{
		"nothing given":  `{"tickets":[{"type":"deutschlandticket"}]}`,
		"no departure":   `{"from":"Rottenburg","to":"Eyach","tickets":[{"type":"deutschlandticket"}]}`,
		"bad departure":  `{"from":"Rottenburg","to":"Eyach","departure":"morgen","tickets":[{"type":"deutschlandticket"}]}`,
		"unknown stop":   `{"from":"Rottenburg","to":"Nirgendwo","departure":"2026-11-23T10:40","tickets":[{"type":"deutschlandticket"}]}`,
		"arrival search": `{"link":"` + searchLink[:len(searchLink)-1] + `A","tickets":[{"type":"deutschlandticket"}]}`,
	} {
		if code, out := post(t, service, body); code != http.StatusBadRequest || len(out["error"]) == 0 {
			t.Errorf("%s: status %d, %s", name, code, out["error"])
		}
	}
}

// The pasted answer of bahn.de's lookup names the exact connection and is
// decoded without any request to bahn.de.
func TestConnectionFromPastedLookup(t *testing.T) {
	raw, err := os.ReadFile("../dbapi/testdata/verbindung_3legs.json")
	if err != nil {
		t.Fatal(err)
	}
	var lookup struct {
		HinfahrtRecon string `json:"hinfahrtRecon"`
	}
	if err := json.Unmarshal(raw, &lookup); err != nil {
		t.Fatal(err)
	}
	var calls []int
	service := &Service{DB: fakeDB{err: errors.New("bahn.de must not be asked")}, Tariff: fakeTariff{&calls}}
	for name, recon := range map[string]string{"whole answer": string(raw), "recon string only": lookup.HinfahrtRecon} {
		body, _ := json.Marshal(Request{Recon: recon, Tickets: []check.Ticket{{Type: check.TypeDeutschlandticket}}})
		code, out := post(t, service, string(body))
		if code != http.StatusOK {
			t.Errorf("%s: status %d: %s", name, code, out["error"])
			continue
		}
		var connection journey.Journey
		if err := json.Unmarshal(out["journey"], &connection); err != nil || len(connection.Legs) != 3 || connection.Legs[1].Line != "IC 185" || connection.Destination != "Konstanz" {
			t.Errorf("%s: journey = %+v", name, connection)
		}
		if len(out["note"]) != 0 {
			t.Errorf("%s: an exact connection needs no note: %s", name, out["note"])
		}
	}
	for name, recon := range map[string]string{"not a lookup answer": `{"foo":1}`, "garbage": "hello"} {
		body, _ := json.Marshal(Request{Recon: recon, Tickets: []check.Ticket{{Type: check.TypeDeutschlandticket}}})
		if code, out := post(t, service, string(body)); code != http.StatusBadRequest || !strings.Contains(string(out["error"]), "recon") {
			t.Errorf("%s: status %d, %s", name, code, out["error"])
		}
	}
}

// The address of an open connection carries the connection itself.
func TestConnectionLink(t *testing.T) {
	raw, err := os.ReadFile("../dblink/testdata/connection_link.txt")
	if err != nil {
		t.Fatal(err)
	}
	var calls []int
	service := &Service{DB: fakeDB{err: errors.New("bahn.de must not be asked")}, Tariff: fakeTariff{&calls},
		Timetable: fakeTimetable{line("X", "Y")}}
	body, _ := json.Marshal(Request{Link: strings.TrimSpace(string(raw)), Tickets: []check.Ticket{{Type: check.TypeDeutschlandticket}}})
	code, out := post(t, service, string(body))
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, out["error"])
	}
	var connection journey.Journey
	if err := json.Unmarshal(out["journey"], &connection); err != nil || len(connection.Legs) != 2 {
		t.Fatalf("journey = %+v, %v", connection, err)
	}
	if leg := connection.Legs[0]; leg.Line != "S6" || leg.Product != journey.ProductSBahn || leg.Origin.Name != "Singen(Hohentwiel)" || leg.Destination.Name != "Konstanz" {
		t.Errorf("first leg = %+v", leg)
	}
	if leg := connection.Legs[1]; leg.Line != "IR 2118" || leg.Number != "2118" || leg.Product != journey.ProductIR || leg.Destination.Name != "Kreuzlingen" ||
		leg.Departure.UTC().Format("15:04") != "10:09" {
		t.Errorf("second leg = %+v", leg)
	}
	if len(out["note"]) != 0 {
		t.Errorf("an exact connection needs no note: %s", out["note"])
	}
}

// bahn.de's whole-city destinations ("MÜNCHEN") point at the city centre;
// the main station is used instead.
func TestCityPlaceholder(t *testing.T) {
	for name, want := range map[string]bool{"MÜNCHEN": true, "FRANKFURT(MAIN)": true, "München": false, "Horb": false, "S": false, "12": false} {
		if isCityPlaceholder(name) != want {
			t.Errorf("isCityPlaceholder(%q) != %v", name, want)
		}
	}
	service := &Service{Tariff: routeTariff{}, Timetable: fakeTimetable{line("Rottenburg", "Eyach")}, Stops: fakeStops{}}
	link := func(city, lon, lat string) string {
		return "https://www.bahn.de/buchung/fahrplan/suche#so=Rottenburg&zo=" + city +
			"&soid=A%3D1%40O%3DRottenburg%40X%3D8935547%40Y%3D48472434%40L%3D8005197%40" +
			"&zoid=A%3D1%40O%3D" + city + "%40X%3D" + lon + "%40Y%3D" + lat + "%40L%3D8096013%40&hd=2026-11-23T10:40:12&hza=D"
	}
	note := func(searchLink string) string {
		code, out := post(t, service, `{"link":"`+searchLink+`","tickets":[{"type":"deutschlandticket"}]}`)
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, out["error"])
		}
		return string(out["note"])
	}
	// The fake stop search places every stop at 48.5, 9.05.
	if got := note(link("TESTSTADT", "9060000", "48510000")); !strings.Contains(got, "TESTSTADT") || !strings.Contains(got, "TESTSTADT Hbf genommen") {
		t.Errorf("city placeholder: note = %s", got)
	}
	// A main station found far away from the placeholder is another place.
	if got := note(link("TESTSTADT", "13400000", "52500000")); strings.Contains(got, "ganze Stadt") {
		t.Errorf("far match was used: note = %s", got)
	}
	if got := note(link("Teststadt", "9060000", "48510000")); strings.Contains(got, "ganze Stadt") {
		t.Errorf("ordinary stop treated as city: note = %s", got)
	}
}

func TestProgress(t *testing.T) {
	full := line("Rottenburg", "Bieringen", "Eyach", "Horb")
	var logged []string
	service := &Service{DB: fakeDB{connection: withoutStops(full)}, Tariff: routeTariff{}, Timetable: fakeTimetable{full}, Stops: fakeStops{},
		Log: func(msg string) { logged = append(logged, msg) }}
	body := `{"link":"` + link + `","tickets":[{"type":"verbund","from":"Rottenburg","to":"Eyach"}]}`
	post := func(target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.Header.Set("Accept", "text/markdown")
		rec := httptest.NewRecorder()
		service.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Streamed: progress lines first, then the document.
	rec := post("/v1/check?progress=1", body)
	out := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(out, "Ablauf:\n\n- ") {
		t.Fatalf("status %d, body:\n%s", rec.Code, out)
	}
	doc := strings.Index(out, "\n# Rottenburg → Horb\n")
	if doc < 0 {
		t.Fatalf("result document missing:\n%s", out)
	}
	for _, want := range []string{"Ticket Verbundticket (Rottenburg – Eyach): Zonen für die Strecke ermitteln", "Ticket aufgelöst: Verbundticket naldo, Zonen 12, 112, 17", "Tickets prüfen (1)", "fertig nach "} {
		if index := strings.Index(out, want); index < 0 || index > doc {
			t.Errorf("progress lacks %q before the result:\n%s", want, out[:doc])
		}
	}
	if len(logged) == 0 {
		t.Error("server log received nothing")
	}

	// Not streamed by default, but still logged on the server.
	logged = nil
	rec = post("/v1/check", body)
	if strings.Contains(rec.Body.String(), "Ablauf:") || !strings.HasPrefix(rec.Body.String(), "# ") || len(logged) == 0 {
		t.Errorf("default markdown: logged %d, body:\n%s", len(logged), rec.Body.String())
	}

	// A failure while streaming is reported in the body.
	rec = post("/v1/check?progress=1", `{"link":"nope","tickets":[{"type":"deutschlandticket"}]}`)
	if !strings.Contains(rec.Body.String(), "\nFehler: ") {
		t.Errorf("streamed error: %s", rec.Body.String())
	}
	// JSON never carries progress lines.
	req := httptest.NewRequest(http.MethodPost, "/v1/check?progress=1", strings.NewReader(body))
	rec = httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	if !json.Valid(rec.Body.Bytes()) {
		t.Errorf("JSON with progress=1 is not valid JSON: %s", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Service{}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}
