// Package api wires link resolution, tariff assessment and ticket checks
// together and exposes them as a JSON HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/check"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dbapi"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dblink"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/progress"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

// Resolver turns a share reference into a journey.
type Resolver interface {
	Resolve(ctx context.Context, ref dblink.Ref) (journey.Journey, error)
}

// StopFinder resolves a stop name to a station with a position.
type StopFinder interface {
	FindStop(ctx context.Context, name string) (journey.Stop, error)
}

// Timetable supplies what a share link does not carry: the stops of a ride,
// and connections between two stops.
type Timetable interface {
	Enrich(ctx context.Context, leg journey.Leg) (journey.Leg, error)
	// Route finds the usual local connection, for tickets given by route.
	Route(ctx context.Context, from, to journey.Stop, when time.Time) (journey.Journey, error)
	// Connection finds the connection departing at the given time, or the
	// next one after it.
	Connection(ctx context.Context, from, to journey.Stop, departure time.Time) (journey.Journey, error)
}

// Service is the core use case: link + tickets in, verdicts out.
type Service struct {
	DB     Resolver
	Tariff tariff.Source
	// Timetable adds intermediate stops to the legs. Without it, checks
	// still work but cannot say how far a ticket is valid within a leg.
	Timetable Timetable
	// Stops resolves the names of tickets described by from/to; together
	// with Timetable it finds their route.
	Stops StopFinder
	// Log, if set, receives the progress messages of every HTTP request.
	Log func(msg string)
	// Cache, if set, keeps the zones found for tickets described by route.
	Cache *cache.Cache
}

// ticketTTL is how long the zones of a route ticket are reused.
const ticketTTL = 7 * 24 * time.Hour

// Request is the input of a check.
type Request struct {
	// Link is a bahn.de link, or text containing one: a share link of one
	// connection, or a search link (start, destination and time).
	Link string `json:"link,omitempty"`
	// From, To and Departure name the connection without a link: two stop
	// names and the departure time, e.g. "2026-11-23T10:45" (German time).
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Departure string `json:"departure,omitempty"`
	// Recon is what bahn.de's share-link lookup returns, pasted in: either
	// the whole JSON answer or just its "hinfahrtRecon" string. It names
	// the exact connection and is decoded without asking bahn.de.
	Recon   string         `json:"recon,omitempty"`
	Tickets []check.Ticket `json:"tickets"`
}

// Response is the outcome of a check.
type Response struct {
	// Note says how the connection was chosen when the request did not
	// name exactly one (search link, or from/to/departure).
	Note    string          `json:"note,omitempty"`
	Journey journey.Journey `json:"journey"`
	// Tariff holds the fare source's answer per leg, parallel to journey.legs.
	Tariff []tariff.Assessment `json:"tariff"`
	// Verdict and Coverage combine all tickets: whether together they
	// cover the journey, and per leg which stretch is covered by which
	// tickets (indexes into results) and where a gap remains.
	Verdict  check.Verdict       `json:"verdict"`
	Coverage []check.LegCoverage `json:"coverage"`
	// Results holds the outcome per ticket.
	Results []check.Result `json:"results"`
}

// InputError marks problems caused by the request itself.
type InputError struct{ Err error }

func (inputError *InputError) Error() string { return inputError.Err.Error() }
func (inputError *InputError) Unwrap() error { return inputError.Err }

// Check resolves the link, assesses every ride and evaluates all tickets.
func (service *Service) Check(ctx context.Context, req Request) (Response, error) {
	if len(req.Tickets) == 0 {
		return Response{}, &InputError{errors.New("at least one ticket is required")}
	}
	load, err := service.connection(req)
	if err != nil {
		return Response{}, &InputError{err}
	}
	tickets := make([]check.Ticket, len(req.Tickets))
	ticketErrs := make([]error, len(req.Tickets))
	each(len(tickets), func(index int) {
		tickets[index], ticketErrs[index] = service.ResolveTicket(ctx, req.Tickets[index])
	})
	for index, err := range ticketErrs {
		if err != nil {
			return Response{}, &InputError{fmt.Errorf("tickets[%d]: %w", index, err)}
		}
	}
	connection, note, err := load(ctx)
	if err != nil {
		return Response{}, err
	}
	resp := Response{Note: note, Journey: connection, Tariff: service.assess(ctx, connection)}
	progress.Logf(ctx, "Tickets prüfen (%d)", len(tickets))
	partial := service.cachedAssess()
	for _, ticket := range tickets {
		result := check.Check(ticket, connection, resp.Tariff)
		result.RefineVerbund(ctx, connection, resp.Tariff, partial)
		resp.Results = append(resp.Results, result)
	}
	if err := ctx.Err(); err != nil {
		// Stopped or timed out: the answers were cut off, so the verdicts
		// would claim gaps that are not there.
		return Response{}, err
	}
	resp.Coverage = check.Coverage(connection, resp.Results)
	resp.Verdict = check.CoverageVerdict(resp.Coverage)
	return resp, nil
}

// each runs run for every index below count at the same time and waits for all.
func each(count int, run func(index int)) {
	var waitGroup sync.WaitGroup
	for index := range count {
		waitGroup.Go(func() { run(index) })
	}
	waitGroup.Wait()
}

// loader fetches the connection of a request. Its note says how the
// connection was chosen when the request did not name exactly one.
type loader func(ctx context.Context) (connection journey.Journey, note string, err error)

// connection validates how the request names the connection and returns
// the function that loads it. Three ways, in this order: a pasted lookup
// answer ("recon"), a link, or from/to/departure.
func (service *Service) connection(req Request) (loader, error) {
	switch {
	case strings.TrimSpace(req.Recon) != "":
		return service.fromRecon(strings.TrimSpace(req.Recon))
	case strings.TrimSpace(req.Link) != "":
		return service.fromLink(req.Link)
	case req.From == "" || req.To == "" || req.Departure == "":
		return nil, errors.New("give a link, or from, to and departure, or recon")
	}
	return service.fromStops(req.From, req.To, req.Departure)
}

// fromRecon reads the connection from bahn.de's lookup answer: the whole
// JSON or just its "hinfahrtRecon" string.
func (service *Service) fromRecon(recon string) (loader, error) {
	if strings.HasPrefix(recon, "{") {
		var lookup struct {
			HinfahrtRecon string `json:"hinfahrtRecon"`
		}
		if err := json.Unmarshal([]byte(recon), &lookup); err != nil || lookup.HinfahrtRecon == "" {
			return nil, errors.New("recon: expected the JSON of bahn.de's share-link lookup, containing hinfahrtRecon")
		}
		recon = lookup.HinfahrtRecon
	}
	return service.decoded(recon, "recon", "aus der eingefügten bahn.de-Antwort")
}

// decoded returns the loader for a connection in bahn.de's encoded form.
// what names the input in errors, origin in the progress message.
func (service *Service) decoded(recon, what, origin string) (loader, error) {
	connection, err := dbapi.DecodeRecon(recon)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return func(ctx context.Context) (journey.Journey, string, error) {
		progress.Logf(ctx, "Verbindung %s: %s → %s, %d Abschnitt(e)", origin, connection.Origin, connection.Destination, len(connection.Legs))
		return service.enrich(ctx, connection), "", nil
	}, nil
}

// fromLink reads a pasted link: a connection link carries the connection
// itself, a share link is looked up at bahn.de, and a search link names
// start, destination and time.
func (service *Service) fromLink(link string) (loader, error) {
	ref, err := dblink.Parse(link)
	switch {
	case err != nil:
		return nil, err
	case ref.Recon != "":
		return service.decoded(ref.Recon, "connection link", "aus dem Link gelesen")
	case ref.Search == nil:
		return func(ctx context.Context) (journey.Journey, string, error) {
			connection, err := service.DB.Resolve(ctx, ref)
			if err != nil {
				return connection, "", err
			}
			return service.enrich(ctx, connection), "", nil
		}, nil
	case service.Timetable == nil:
		return nil, errors.New("search links are not supported by this server")
	}
	return func(ctx context.Context) (journey.Journey, string, error) {
		from, fromNote := service.mainStation(ctx, ref.Search.From)
		to, toNote := service.mainStation(ctx, ref.Search.To)
		connection, note, err := service.search(ctx, from, to, ref.Search.Departure, "der Suchlink")
		return connection, note + fromNote + toNote, err
	}, nil
}

// fromStops takes the connection between two stop names at a given time.
func (service *Service) fromStops(from, to, departure string) (loader, error) {
	if service.Timetable == nil || service.Stops == nil {
		return nil, errors.New("connections given by from/to/departure are not supported by this server")
	}
	dep, err := parseDeparture(departure)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (journey.Journey, string, error) {
		fromStop, toStop, err := service.findStops(ctx, from, to)
		if err != nil {
			return journey.Journey{}, "", &InputError{err}
		}
		return service.search(ctx, fromStop, toStop, dep, "die Anfrage")
	}, nil
}

// search takes the connection departing at dep, or the next one. source
// names what asked for it, for the note.
func (service *Service) search(ctx context.Context, from, to journey.Stop, dep time.Time, source string) (journey.Journey, string, error) {
	connection, err := service.Timetable.Connection(ctx, from, to, dep)
	note := fmt.Sprintf("Es wurde die erste Verbindung ab %s Uhr genommen (%s nennt keine bestimmte Verbindung).", clock(dep), source)
	return connection, note, err
}

// findStops resolves two stop names.
func (service *Service) findStops(ctx context.Context, from, to string) (fromStop, toStop journey.Stop, err error) {
	if fromStop, err = service.Stops.FindStop(ctx, from); err != nil {
		return fromStop, toStop, err
	}
	toStop, err = service.Stops.FindStop(ctx, to)
	return fromStop, toStop, err
}

// mainStation replaces a city placeholder by the city's main station.
// bahn.de offers whole cities as start or destination, written in capitals
// ("MÜNCHEN"), meaning any of the city's main stations; the position that
// comes with it is the city centre, not a station. Aiming at that position
// would extend the journey by local transport into the centre.
func (service *Service) mainStation(ctx context.Context, stop journey.Stop) (journey.Stop, string) {
	if service.Stops == nil || !isCityPlaceholder(stop.Name) {
		return stop, ""
	}
	hbf, err := service.Stops.FindStop(ctx, stop.Name+" Hbf")
	// The match must lie in that city; anything else is another place.
	if err != nil || !hbf.HasCoord() || hbf.DistanceTo(stop) > 15000 {
		return stop, ""
	}
	progress.Logf(ctx, "%q steht für die ganze Stadt: %s genommen", stop.Name, hbf.Name)
	return hbf, fmt.Sprintf(" %q steht bei der Bahn für die ganze Stadt; hier wurde %s genommen.", stop.Name, hbf.Name)
}

// isCityPlaceholder reports whether a stop name is written in capitals
// throughout, as bahn.de writes whole cities.
func isCityPlaceholder(name string) bool {
	letters := 0
	for _, char := range name {
		if unicode.IsLetter(char) {
			letters++
			if !unicode.IsUpper(char) {
				return false
			}
		}
	}
	return letters >= 2
}

// parseDeparture accepts a time with zone, or a German local time.
func parseDeparture(text string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if parsed, err := time.ParseInLocation(layout, text, journey.Berlin); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("departure %q is not a time like 2026-11-23T10:45", text)
}

// ResolveTicket fills in net and zones of a Verbund ticket that is described
// by its route: either two stop names or a DB share link of the route.
// Other tickets are returned unchanged.
func (service *Service) ResolveTicket(ctx context.Context, ticket check.Ticket) (check.Ticket, error) {
	if err := check.Validate(ticket); err != nil {
		return ticket, err
	}
	if !ticket.NeedsZoneLookup() {
		return ticket.Normalized(), nil
	}
	key := "ticket|" + strings.ToLower(ticket.Net+"|"+ticket.From+"|"+ticket.To+"|"+ticket.Link)
	var cached struct {
		Net   string
		Zones []string
	}
	if service.Cache.Get(key, &cached) && len(cached.Zones) > 0 {
		ticket.Net, ticket.Zones = cached.Net, cached.Zones
		progress.Logf(ctx, "Ticket aus dem Cache: %s", TicketLabel(ticket))
		return ticket, nil
	}
	route, err := service.ticketRoute(ctx, ticket)
	if err != nil {
		return ticket, err
	}
	resolved, err := ticket.WithZones(service.routeZones(ctx, route))
	if err != nil {
		return resolved, err
	}
	progress.Logf(ctx, "Ticket aufgelöst: %s", TicketLabel(resolved))
	cached.Net, cached.Zones = resolved.Net, resolved.Zones
	service.Cache.Set(key, cached, ticketTTL)
	return resolved, nil
}

// ticketRoute finds the route a ticket is described by: the connection of
// its share link, or the usual connection between its two stops.
func (service *Service) ticketRoute(ctx context.Context, ticket check.Ticket) (journey.Journey, error) {
	if ticket.Link != "" {
		progress.Logf(ctx, "Ticket %s: Zonen aus dem Verbindungslink ermitteln", TicketLabel(ticket))
		ref, err := dblink.Parse(ticket.Link)
		if err != nil {
			return journey.Journey{}, fmt.Errorf("ticket link: %w", err)
		}
		route, err := service.DB.Resolve(ctx, ref)
		if err != nil {
			return journey.Journey{}, fmt.Errorf("ticket link: %w", err)
		}
		return service.enrich(ctx, route), nil
	}
	if service.Stops == nil || service.Timetable == nil {
		return journey.Journey{}, errors.New("tickets described by from/to are not supported by this server")
	}
	progress.Logf(ctx, "Ticket %s: Zonen für die Strecke ermitteln", TicketLabel(ticket))
	from, to, err := service.findStops(ctx, ticket.From, ticket.To)
	if err != nil {
		return journey.Journey{}, err
	}
	// A weekday mid-morning connection stands for "the usual route".
	return service.Timetable.Route(ctx, from, to, nextWeekdayMorning(time.Now()))
}

// enrich adds the stop lists to all rides that lack them, in parallel. A
// leg the timetable source cannot find is kept as it is.
func (service *Service) enrich(ctx context.Context, connection journey.Journey) journey.Journey {
	if service.Timetable == nil {
		return connection
	}
	legs := slices.Clone(connection.Legs)
	each(len(legs), func(index int) {
		if legs[index].Walk || len(legs[index].Stops) >= 2 {
			return
		}
		if enriched, err := service.Timetable.Enrich(ctx, legs[index]); err == nil {
			legs[index] = enriched
		}
	})
	connection.Legs = legs
	return connection
}

// routeZones collects the zones of every stop along a route. Planners may
// report only the zones at both ends of a ride, so the ride from the start
// of each leg to each of its later stops is assessed and the zones united.
func (service *Service) routeZones(ctx context.Context, route journey.Journey) []tariff.ZoneSet {
	var rides []journey.Leg
	for _, leg := range route.Legs {
		if leg.Walk {
			continue
		}
		rides = append(rides, leg)
		for stopIndex := 1; stopIndex < len(leg.Stops)-1; stopIndex++ {
			if sub, ok := leg.Sub(0, stopIndex); ok {
				rides = append(rides, sub)
			}
		}
	}
	progress.Logf(ctx, "Zonen entlang der Ticketstrecke: %d Teilstrecken abfragen", len(rides))
	answers := make([]tariff.Assessment, len(rides))
	limit := make(chan struct{}, 4) // be gentle with the planners
	each(len(rides), func(index int) {
		limit <- struct{}{}
		defer func() { <-limit }()
		answers[index] = service.Tariff.Assess(ctx, rides[index])
	})
	var zones []tariff.ZoneSet
	for _, assessment := range answers {
		zones = append(zones, assessment.Zones...)
	}
	return zones
}

func nextWeekdayMorning(now time.Time) time.Time {
	day := now.In(journey.Berlin).AddDate(0, 0, 1)
	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, 1)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, journey.Berlin)
}

// cachedAssess remembers answers for partial rides within one request, so
// several tickets probing the same leg do not repeat planner queries.
func (service *Service) cachedAssess() check.AssessFunc {
	type key struct {
		leg      int
		from, to string
		dep      int64
	}
	cache := map[key]tariff.Assessment{}
	return func(ctx context.Context, leg journey.Leg) tariff.Assessment {
		cacheKey := key{leg.Index, leg.Origin.ID + "|" + leg.Origin.Name, leg.Destination.ID + "|" + leg.Destination.Name, leg.Departure.Unix()}
		if assessment, ok := cache[cacheKey]; ok {
			return assessment
		}
		assessment := service.Tariff.Assess(ctx, leg)
		cache[cacheKey] = assessment
		return assessment
	}
}

// assess queries the fare source for all rides in parallel. Long-distance
// trains are included: some also run under a local designation.
func (service *Service) assess(ctx context.Context, connection journey.Journey) []tariff.Assessment {
	out := make([]tariff.Assessment, len(connection.Legs))
	each(len(connection.Legs), func(index int) {
		if connection.Legs[index].Walk {
			out[index].Note = "walk"
			return
		}
		out[index] = service.Tariff.Assess(ctx, connection.Legs[index])
	})
	return out
}

// Handler returns the HTTP API:
//
//	POST /v1/check            Request -> Response
//	POST /v1/tickets/resolve  Ticket described by route -> Ticket with net and zones
//	GET  /healthz
//
// /v1/check answers in Markdown instead of JSON when the request asks for it
// with "Accept: text/markdown" (or text/plain) or "?format=markdown". Adding
// "?progress=1" to such a request streams progress lines ahead of the result.
func (service *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/tickets/resolve", func(writer http.ResponseWriter, request *http.Request) {
		var ticket check.Ticket
		if !readJSON(writer, request, &ticket) {
			return
		}
		resolved, err := service.ResolveTicket(request.Context(), ticket)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, resolved)
	})
	mux.HandleFunc("POST /v1/check", service.handleCheck)
	return mux
}

func (service *Service) handleCheck(writer http.ResponseWriter, request *http.Request) {
	var checkRequest Request
	if !readJSON(writer, request, &checkRequest) {
		return
	}
	start := time.Now()
	markdown := wantsMarkdown(request)
	// With "?progress=1" a Markdown client gets the progress messages
	// streamed ahead of the result. The status line is then already
	// sent, so a later failure is reported in the body only.
	stream := markdown && wantsProgress(request)
	flusher, _ := writer.(http.Flusher)
	ctx := progress.With(request.Context(), func(msg string) {
		if service.Log != nil {
			service.Log(msg)
		}
		if stream {
			fmt.Fprintf(writer, "- %s\n", msg)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	if stream {
		writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		fmt.Fprint(writer, "Ablauf:\n\n")
	}
	resp, err := service.Check(ctx, checkRequest)
	took := time.Since(start).Round(10 * time.Millisecond)
	if err != nil {
		progress.Logf(ctx, "Fehler nach %s: %v", took, err)
	} else {
		progress.Logf(ctx, "fertig nach %s", took)
	}
	switch {
	case stream && err != nil:
		fmt.Fprintf(writer, "\nFehler: %s\n", err)
	case stream:
		fmt.Fprintf(writer, "\n%s", resp.Markdown())
	case err != nil && markdown:
		writeText(writer, errorStatus(err), "Fehler: "+err.Error()+"\n")
	case err != nil:
		writeError(writer, errorStatus(err), err.Error())
	case markdown:
		writeText(writer, http.StatusOK, resp.Markdown())
	default:
		writeJSON(writer, http.StatusOK, resp)
	}
}

// errorStatus maps a failed check to its HTTP status.
func errorStatus(err error) int {
	var inputError *InputError
	switch {
	case errors.As(err, &inputError):
		return http.StatusBadRequest
	case errors.Is(err, dbapi.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, dbapi.ErrBlocked):
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

// readJSON decodes the request body into v. On failure it answers the
// request and reports false.
func readJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

// wantsMarkdown reports whether the client asked for the text rendering.
func wantsMarkdown(request *http.Request) bool {
	switch strings.ToLower(request.URL.Query().Get("format")) {
	case "markdown", "md", "text":
		return true
	case "json":
		return false
	}
	accept := strings.ToLower(request.Header.Get("Accept"))
	return strings.Contains(accept, "text/markdown") || strings.Contains(accept, "text/plain")
}

// wantsProgress reports whether the client asked for streamed progress.
func wantsProgress(request *http.Request) bool {
	switch strings.ToLower(request.URL.Query().Get("progress")) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func writeText(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	writer.WriteHeader(status)
	writer.Write([]byte(body))
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, msg string) {
	writeJSON(writer, status, map[string]string{"error": msg})
}
