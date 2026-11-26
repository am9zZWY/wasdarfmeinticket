//go:build js && wasm

// Command wasm runs the ticket check inside a browser. It is compiled to
// WebAssembly and offers its functions to the page as the global object
// "wasdarfmeinticket":
//
//	check(requestJSON, onProgress)  Promise of {view, markdown} as JSON
//	resolveTicket(ticketJSON)       Promise of the ticket with net and zones as JSON
//	suggestStops(text)              Promise of the names of matching stops as JSON
//	describeLink(text)              what a pasted link is, as JSON
//	ticketLabel(ticketJSON)         display name of a ticket
//	laendertickets                  the supported Länderticket names, as JSON
//	verbuende                       the offered Verbund net codes and names, as JSON
//
// Requests go straight from the browser to the upstreams, so only those
// that allow cross-origin requests can be used. bahn.de does not: a share
// link cannot be looked up here, see describeLink.
package main

import (
	"context"
	"encoding/json"
	"slices"
	"syscall/js"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/api"
	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/check"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dbapi"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dblink"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/progress"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff/efa"
	"github.com/am9zZWY/wasdarfmeinticket/internal/timetable/transitous"
)

// checkTimeout bounds one call from the page.
const checkTimeout = 3 * time.Minute

// storageKey is where the cache is kept in the browser's local storage.
const storageKey = "wasdarfmeinticket.cache"

func main() {
	store := cache.New(loadStored(), saveStored)
	db := dbapi.New()
	db.Cache = store
	timetable := transitous.New()
	timetable.Cache = store
	planners := efa.New(journey.Berlin)
	// Planners without CORS headers are blocked by the browser; asking them
	// only fails, once per leg.
	planners.Planners = slices.DeleteFunc(planners.Planners, func(planner efa.Planner) bool { return !planner.Browser })
	planners.Cache = store
	service := &api.Service{DB: db, Tariff: planners, Timetable: timetable, Stops: timetable, Cache: store}

	js.Global().Set("wasdarfmeinticket", map[string]any{
		"check":          async(func(args []js.Value) (string, error) { return runCheck(service, args) }),
		"resolveTicket":  async(func(args []js.Value) (string, error) { return resolveTicket(service, args) }),
		"suggestStops":   async(func(args []js.Value) (string, error) { return suggestStops(timetable, text(args, 0)) }),
		"describeLink":   js.FuncOf(func(_ js.Value, args []js.Value) any { return describeLink(text(args, 0)) }),
		"ticketLabel":    js.FuncOf(func(_ js.Value, args []js.Value) any { return ticketLabel(text(args, 0)) }),
		"laendertickets": encode(check.LaenderticketOptions()),
		"verbuende":      encode(check.VerbundOptions()),
	})
	select {} // keep the functions alive for the page
}

// runCheck performs a check. The second argument, if it is a function,
// receives every progress message.
func runCheck(service *api.Service, args []js.Value) (string, error) {
	var request api.Request
	if err := json.Unmarshal([]byte(text(args, 0)), &request); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	if len(args) > 1 && args[1].Type() == js.TypeFunction {
		ctx = progress.With(ctx, func(message string) { args[1].Invoke(message) })
	}
	response, err := service.Check(ctx, request)
	if err != nil {
		return "", err
	}
	return encode(map[string]any{"view": response.View(), "markdown": response.Markdown()}), nil
}

// resolveTicket looks up net and zones of a ticket described by its route.
func resolveTicket(service *api.Service, args []js.Value) (string, error) {
	var ticket check.Ticket
	if err := json.Unmarshal([]byte(text(args, 0)), &ticket); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	resolved, err := service.ResolveTicket(ctx, ticket)
	if err != nil {
		return "", err
	}
	return encode(resolved), nil
}

// suggestTimeout bounds one lookup of suggestions: an answer that comes
// later is of no use to someone typing.
const suggestTimeout = 5 * time.Second

// suggestStops returns the names of the stops matching a partly typed
// text, without repeats, best first.
func suggestStops(timetable *transitous.Client, input string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
	defer cancel()
	stops, err := timetable.SuggestStops(ctx, input)
	if err != nil {
		return "", err
	}
	names := []string{}
	for _, stop := range stops {
		if !slices.Contains(names, stop.Name) {
			names = append(names, stop.Name)
		}
	}
	return encode(names), nil
}

// describeLink says what kind of link a pasted text contains. For a share
// link it also returns the address of bahn.de's lookup: the browser may
// open it, but the page may not read it, so the user copies its content
// and hands it in as "recon".
func describeLink(input string) string {
	ref, err := dblink.Parse(input)
	switch {
	case err != nil:
		return encode(map[string]string{"kind": "none", "error": err.Error()})
	case ref.VBID != "":
		return encode(map[string]string{"kind": "share", "lookupURL": dbapi.DefaultBaseURL + "/angebote/verbindung/" + ref.VBID})
	case ref.Recon != "":
		return encode(map[string]string{"kind": "connection"})
	}
	return encode(map[string]string{"kind": "search"})
}

func ticketLabel(ticketJSON string) string {
	var ticket check.Ticket
	if err := json.Unmarshal([]byte(ticketJSON), &ticket); err != nil {
		return ""
	}
	return api.TicketLabel(ticket.Normalized())
}

// async wraps a blocking function as a JavaScript function that returns a
// Promise: resolved with the function's result, rejected with its error.
func async(work func(args []js.Value) (string, error)) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) any {
		var executor js.Func
		executor = js.FuncOf(func(_ js.Value, settle []js.Value) any {
			resolve, reject := settle[0], settle[1]
			go func() {
				defer executor.Release()
				result, err := work(args)
				if err != nil {
					reject.Invoke(js.Global().Get("Error").New(err.Error()))
					return
				}
				resolve.Invoke(result)
			}()
			return nil
		})
		return js.Global().Get("Promise").New(executor)
	})
}

// text returns the argument at index as a string, or "".
func text(args []js.Value, index int) string {
	if index >= len(args) || args[index].Type() != js.TypeString {
		return ""
	}
	return args[index].String()
}

func encode(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(data)
}

// loadStored reads the cache from local storage. Where there is none
// (private mode, or outside a browser) the cache starts empty.
func loadStored() (stored []byte) {
	defer func() { recover() }() // storage access may throw
	if item := js.Global().Get("localStorage").Call("getItem", storageKey); item.Type() == js.TypeString {
		return []byte(item.String())
	}
	return nil
}

// saveStored writes the cache to local storage; failures are ignored.
func saveStored(data []byte) {
	defer func() { recover() }() // storage may be missing or full
	js.Global().Get("localStorage").Call("setItem", storageKey, string(data))
}
