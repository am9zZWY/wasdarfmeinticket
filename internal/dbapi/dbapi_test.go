package dbapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dblink"
	"github.com/am9zZWY/wasdarfmeinticket/internal/httpx"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
)

const (
	vbid      = "a30982d3-9bbf-492d-828c-43a46ce38546" // Tübingen - Stuttgart, one train
	vbid3Legs = "3cc2ab95-844a-4806-b855-e99de3f17666" // Rottenburg - Konstanz via an IC
)

func serveFixture(t *testing.T, writer http.ResponseWriter, status int, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Error(err)
	}
	writer.WriteHeader(status)
	writer.Write(data)
}

func testClient(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /angebote/verbindung/{id}", func(writer http.ResponseWriter, request *http.Request) {
		switch request.PathValue("id") {
		case vbid:
			serveFixture(t, writer, http.StatusOK, "testdata/verbindung.json")
		case vbid3Legs:
			serveFixture(t, writer, http.StatusOK, "testdata/verbindung_3legs.json")
		default:
			writer.WriteHeader(http.StatusInternalServerError)
		}
	})
	// Anything else, in particular the POST calls bahn.de blocks, must not be used.
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		writer.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 1, time.Millisecond
	return &Client{BaseURL: srv.URL, HTTP: httpClient}
}

func berlin(day, hour, min int) time.Time {
	return time.Date(2026, 11, day, hour, min, 0, 0, journey.Berlin)
}

func TestResolve(t *testing.T) {
	connection, err := testClient(t).Resolve(context.Background(), dblink.Ref{VBID: vbid})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Origin != "Tübingen Hbf" || connection.Destination != "Stuttgart Hbf" || len(connection.Legs) != 1 {
		t.Fatalf("unexpected journey: %+v", connection)
	}
	leg := connection.Legs[0]
	if leg.Walk || leg.Product != journey.ProductRegional || leg.Category != "NRE" || leg.Number != "19382" || leg.Line != "" {
		t.Errorf("unexpected leg: %+v", leg)
	}
	if !leg.Departure.Equal(berlin(24, 9, 58)) || !leg.Arrival.Equal(berlin(24, 10, 56)) {
		t.Errorf("times = %v - %v", leg.Departure, leg.Arrival)
	}
	if leg.Origin.ID != "8000141" || leg.Origin.Lat != 48.515645 || leg.Origin.Lon != 9.055392 {
		t.Errorf("origin = %+v", leg.Origin)
	}
	if leg.Destination.Name != "Stuttgart Hbf" || leg.Destination.ID != "8000096" || !leg.Destination.HasCoord() {
		t.Errorf("destination = %+v", leg.Destination)
	}
}

func TestResolveThreeLegs(t *testing.T) {
	connection, err := testClient(t).Resolve(context.Background(), dblink.Ref{VBID: vbid3Legs})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Origin != "Rottenburg(Neckar)" || connection.Destination != "Konstanz" || len(connection.Legs) != 3 {
		t.Fatalf("unexpected journey: %+v", connection)
	}
	want := []struct {
		product, category, line, number, from, to string
		dep, arr                                  time.Time
	}{
		{journey.ProductRegional, "NRB", "", "32503", "Rottenburg(Neckar)", "Horb", berlin(23, 10, 45), berlin(23, 11, 10)},
		{journey.ProductECIC, "ICd", "IC 185", "185", "Horb", "Singen(Hohentwiel)", berlin(23, 11, 14), berlin(23, 12, 26)},
		{journey.ProductSBahn, "DRS", "S6", "", "Singen(Hohentwiel)", "Konstanz", berlin(23, 12, 32), berlin(23, 13, 5)},
	}
	for index, expected := range want {
		leg := connection.Legs[index]
		if leg.Index != index || leg.Walk || leg.Product != expected.product || leg.Category != expected.category || leg.Line != expected.line || leg.Number != expected.number ||
			leg.Origin.Name != expected.from || leg.Destination.Name != expected.to || !leg.Departure.Equal(expected.dep) || !leg.Arrival.Equal(expected.arr) {
			t.Errorf("leg %d = %+v", index, leg)
		}
		if !leg.Origin.HasCoord() || !leg.Destination.HasCoord() || len(leg.Origin.ID) != 7 {
			t.Errorf("leg %d stops = %+v / %+v", index, leg.Origin, leg.Destination)
		}
	}
	if !connection.Legs[1].LongDistance() || connection.Legs[0].LongDistance() {
		t.Error("long-distance classification wrong")
	}
}

func TestDecodeRecon(t *testing.T) {
	loc := func(name, eva string) string { return "A=1@O=" + name + "@X=9000000@Y=48000000@L=" + eva + "@a=128@" }
	// Without ride details only long-distance trains are recognised, and a
	// non-ride record is a walk.
	recon := "¶HKI¶T$" + loc("A", "8000001") + "$" + loc("B", "8000002") + "$202611231000$202611231100$ICE          71$$1$$$$$$" +
		"§G@F$" + loc("B", "8000002") + "$" + loc("C", "8000003") + "$202611231100$202611231105$$$1$$$$$$" +
		"§T$" + loc("C", "8000003") + "$" + loc("D", "8000004") + "$202611231110$202611231130$             4711$$1$$$$$$¶KC¶#VE#2#"
	connection, err := DecodeRecon(recon)
	if err != nil || len(connection.Legs) != 3 {
		t.Fatalf("journey = %+v, %v", connection, err)
	}
	if leg := connection.Legs[0]; leg.Product != journey.ProductICE || leg.Line != "ICE 71" || leg.Number != "71" {
		t.Errorf("ICE leg = %+v", leg)
	}
	if leg := connection.Legs[1]; !leg.Walk || leg.Product != "" || leg.Origin.Name != "B" {
		t.Errorf("walk leg = %+v", leg)
	}
	if leg := connection.Legs[2]; leg.Walk || leg.Product != "" || leg.Number != "4711" {
		t.Errorf("unclassified leg = %+v", leg)
	}
	for _, bad := range []string{"", "¶KC¶#VE#2#", "¶HKI¶T$a$b", "¶HKI¶T$a$b$nope$202611231130$x$"} {
		if _, err := DecodeRecon(bad); err == nil {
			t.Errorf("DecodeRecon(%q): expected error", bad)
		}
	}
}

func TestBlockedThenAllowed(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			writer.WriteHeader(http.StatusForbidden)
			writer.Write([]byte(`{"status":"ERROR","code":"OPS_BLOCKED"}`))
			return
		}
		serveFixture(t, writer, http.StatusOK, "testdata/verbindung.json")
	}))
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 0, time.Millisecond
	client := &Client{BaseURL: srv.URL, HTTP: httpClient, BlockedRetries: 2, BlockedPause: time.Millisecond}
	connection, err := client.Resolve(context.Background(), dblink.Ref{VBID: vbid})
	if err != nil || len(connection.Legs) != 1 || calls != 3 {
		t.Errorf("journey = %+v, err = %v, calls = %d", connection, err, calls)
	}
}

// A link resolved once is answered from the cache, even while bahn.de
// refuses requests.
func TestResolveCached(t *testing.T) {
	allowed := true
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		if !allowed {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		serveFixture(t, writer, http.StatusOK, "testdata/verbindung_3legs.json")
	}))
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 0, time.Millisecond
	client := &Client{BaseURL: srv.URL, HTTP: httpClient, Cache: cache.Open("")}
	first, err := client.Resolve(context.Background(), dblink.Ref{VBID: vbid3Legs})
	if err != nil || len(first.Legs) != 3 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	allowed = false
	second, err := client.Resolve(context.Background(), dblink.Ref{VBID: vbid3Legs})
	if err != nil || len(second.Legs) != 3 || calls != 1 {
		t.Errorf("second = %+v, %v, calls = %d", second, err, calls)
	}
	if _, err := client.Resolve(context.Background(), dblink.Ref{VBID: vbid}); !errors.Is(err, ErrBlocked) {
		t.Errorf("another link while blocked: %v", err)
	}
}

func TestBlocked(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusForbidden)
		writer.Write([]byte(`{"status":"ERROR","code":"OPS_BLOCKED"}`))
	}))
	t.Cleanup(srv.Close)
	httpClient := httpx.New()
	httpClient.Retries, httpClient.Backoff = 2, time.Millisecond
	client := &Client{BaseURL: srv.URL, HTTP: httpClient, BlockedRetries: 2, BlockedPause: time.Millisecond}
	defer func() {
		if calls != 3 {
			t.Errorf("calls = %d, want 3 and no more", calls)
		}
	}()
	if _, err := client.Resolve(context.Background(), dblink.Ref{VBID: vbid}); !errors.Is(err, ErrBlocked) {
		t.Errorf("resolve: err = %v, want ErrBlocked", err)
	}
}

func TestResolveUnknown(t *testing.T) {
	_, err := testClient(t).Resolve(context.Background(), dblink.Ref{VBID: "00000000-0000-4000-8000-000000000000"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
