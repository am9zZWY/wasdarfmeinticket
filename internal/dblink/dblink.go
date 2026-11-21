// Package dblink reads pasted bahn.de links. Three kinds are understood:
//
//   - share links (".../buchung/start?vbid=<id>") name one connection; the
//     id has to be looked up at bahn.de
//   - connection links, the address bahn.de shows while a connection is
//     open (".../fahrplan/suche#...&gh=<recon>"), carry that connection in
//     encoded form and need no lookup
//   - search links (".../buchung/fahrplan/suche#so=...&zo=...&hd=...") carry
//     only start, destination and time, and need no lookup either
package dblink

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
)

// Ref is what a pasted link points to: exactly one of its fields is set.
type Ref struct {
	// VBID identifies a shared connection on bahn.de.
	VBID string
	// Recon is one connection in bahn.de's encoded form, taken from a
	// connection link.
	Recon string
	// Search is a connection search read from a search link.
	Search *Search
}

// Search is a connection search: from, to and the earliest departure.
type Search struct {
	From journey.Stop
	To   journey.Stop
	// Departure is the time the search starts at.
	Departure time.Time
}

// ErrNoLink is returned when the input contains no usable connection link.
var ErrNoLink = errors.New("no DB connection link found: expected a share link (…?vbid=<id>) or a search link (…/fahrplan/suche#so=…&zo=…&hd=…)")

var (
	urlRe  = regexp.MustCompile(`https?://[^\s<>"']+`)
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Parse finds a bahn.de link in free text (for example the text the DB
// Navigator puts on the clipboard) and returns what it points to. A bare
// vbid is accepted as well.
func Parse(input string) (Ref, error) {
	input = strings.TrimSpace(input)
	if uuidRe.MatchString(input) {
		return Ref{VBID: strings.ToLower(input)}, nil
	}
	var searchErr error
	for _, raw := range urlRe.FindAllString(input, -1) {
		link, err := url.Parse(strings.TrimRight(raw, ".,;)"))
		if err != nil || !isBahnHost(link.Hostname()) {
			continue
		}
		if id := link.Query().Get("vbid"); uuidRe.MatchString(id) {
			return Ref{VBID: strings.ToLower(id)}, nil
		}
		// The web app keeps its parameters in the fragment. An open
		// connection is in "gh". It is read from the raw fragment: the
		// value contains base64, whose "+" a query parser would turn
		// into a space.
		if recon := fragmentValue(link.EscapedFragment(), "gh"); strings.Contains(recon, "¶HKI¶") {
			return Ref{Recon: recon}, nil
		}
		frag, err := url.ParseQuery(link.Fragment)
		if err != nil {
			continue
		}
		if id := frag.Get("vbid"); uuidRe.MatchString(id) {
			return Ref{VBID: strings.ToLower(id)}, nil
		}
		if frag.Has("soid") || frag.Has("zoid") {
			search, err := parseSearch(frag)
			if err != nil {
				searchErr = err
				continue
			}
			return Ref{Search: &search}, nil
		}
	}
	if searchErr != nil {
		return Ref{}, searchErr
	}
	return Ref{}, ErrNoLink
}

// parseSearch reads a search link's parameters: soid/zoid are the stations
// as HAFAS location ids, so/zo their names, hd the time and hza whether
// that time is a departure ("D") or an arrival ("A").
func parseSearch(query url.Values) (Search, error) {
	var search Search
	search.From, search.To = ParseLocation(query.Get("soid")), ParseLocation(query.Get("zoid"))
	if search.From.Name == "" {
		search.From.Name = query.Get("so")
	}
	if search.To.Name == "" {
		search.To.Name = query.Get("zo")
	}
	if !search.From.HasCoord() || !search.To.HasCoord() {
		return Search{}, errors.New("search link lacks the position of start or destination")
	}
	if strings.EqualFold(query.Get("hza"), "A") {
		return Search{}, errors.New("search links by arrival time are not supported; search by departure time or share the connection itself")
	}
	departure, err := time.ParseInLocation("2006-01-02T15:04:05", query.Get("hd"), journey.Berlin)
	if err != nil {
		return Search{}, fmt.Errorf("search link has no usable time (hd=%q)", query.Get("hd"))
	}
	search.Departure = departure.Truncate(time.Minute)
	return search, nil
}

// fragmentValue returns the percent-decoded value of key in a fragment of
// the form "a=1&b=2", or "".
func fragmentValue(fragment, key string) string {
	for _, part := range strings.Split(fragment, "&") {
		if value, ok := strings.CutPrefix(part, key+"="); ok {
			if dec, err := url.PathUnescape(value); err == nil {
				return dec
			}
		}
	}
	return ""
}

func isBahnHost(host string) bool {
	host = strings.ToLower(host)
	return host == "bahn.de" || strings.HasSuffix(host, ".bahn.de")
}

// ParseLocation reads name, id and position from a HAFAS location id
// ("A=1@O=Horb@X=8688982@Y=48441475@L=8000177@", micro degrees).
func ParseLocation(id string) journey.Stop {
	var stop journey.Stop
	for _, field := range strings.Split(id, "@") {
		key, value, _ := strings.Cut(field, "=")
		switch key {
		case "O":
			stop.Name = value
		case "L":
			stop.ID = value
		case "X":
			stop.Lon = microDegrees(value)
		case "Y":
			stop.Lat = microDegrees(value)
		}
	}
	return stop
}

func microDegrees(text string) float64 {
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return float64(value) / 1e6
}
