// Package dbapi resolves bahn.de share links into journeys.
//
// Only one light GET request of bahn.de's web interface is used: the
// share-link lookup. It returns the connection as an encoded string, which
// is decoded here. bahn.de's connection search
// and detail calls are not used; intermediate stops come from the timetable
// source instead.
package dbapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dblink"
	"github.com/am9zZWY/wasdarfmeinticket/internal/httpx"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/progress"
)

// DefaultBaseURL is the public bahn.de web API.
const DefaultBaseURL = "https://www.bahn.de/web/api"

// ErrNotFound means bahn.de does not know the shared connection (any more).
var ErrNotFound = errors.New("bahn.de does not know this shared connection (expired or mistyped link)")

// ErrBlocked means bahn.de refused the request (HTTP 403, "OPS_BLOCKED"):
// its bot protection blocks this client for a while.
var ErrBlocked = errors.New("bahn.de is refusing this tool's requests at the moment (403 OPS_BLOCKED); open https://www.bahn.de/web/api/angebote/verbindung/<vbid> in a browser and pass its content as \"recon\", or give a search link, or from, to and departure")

// Client talks to the bahn.de web API.
type Client struct {
	BaseURL string
	HTTP    *httpx.Client
	// BlockedRetries and BlockedPause control how a refusal (ErrBlocked) is
	// handled: bahn.de's refusals are often intermittent, so the request is
	// repeated a few times, with a pause, before giving up.
	BlockedRetries int
	BlockedPause   time.Duration
	// Cache, if set, keeps the encoded connection of a share link, which
	// never changes.
	Cache *cache.Cache
}

// shareTTL is how long a resolved share link is kept.
const shareTTL = 30 * 24 * time.Hour

// New returns a client for the public bahn.de API.
func New() *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: httpx.New(), BlockedRetries: 2, BlockedPause: 3 * time.Second}
}

// get fetches a URL, repeating the request when bahn.de refuses it.
func (client *Client) get(ctx context.Context, url string, out any) error {
	for attempt := 0; ; attempt++ {
		err := client.HTTP.GetJSON(ctx, url, out)
		if !httpx.IsStatus(err, http.StatusForbidden) {
			return err
		}
		if attempt >= client.BlockedRetries {
			return ErrBlocked
		}
		progress.Logf(ctx, "bahn.de: Anfrage abgelehnt (403), Versuch %d von %d in %s", attempt+2, client.BlockedRetries+1, client.BlockedPause)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(client.BlockedPause):
		}
	}
}

// Resolve turns a share reference into the journey it points to. The legs
// carry trains, end stops and times, but no intermediate stops.
func (client *Client) Resolve(ctx context.Context, ref dblink.Ref) (journey.Journey, error) {
	recon, err := client.recon(ctx, ref.VBID)
	if err != nil {
		return journey.Journey{}, err
	}
	connection, err := DecodeRecon(recon)
	if err != nil {
		return journey.Journey{}, fmt.Errorf("decode shared connection: %w", err)
	}
	client.Cache.Set("share|"+ref.VBID, recon, shareTTL)
	progress.Logf(ctx, "bahn.de: %s → %s, %d Abschnitt(e)", connection.Origin, connection.Destination, len(connection.Legs))
	return connection, nil
}

// recon returns the encoded connection of a share link, from the cache or
// from bahn.de.
func (client *Client) recon(ctx context.Context, vbid string) (string, error) {
	var share struct {
		HinfahrtRecon string `json:"hinfahrtRecon"`
	}
	if client.Cache.Get("share|"+vbid, &share.HinfahrtRecon) {
		progress.Logf(ctx, "bahn.de: Verbindungslink aus dem Cache")
		return share.HinfahrtRecon, nil
	}
	progress.Logf(ctx, "bahn.de: Verbindungslink nachschlagen")
	err := client.get(ctx, client.BaseURL+"/angebote/verbindung/"+vbid, &share)
	switch {
	// bahn.de answers unknown ids with 500.
	case httpx.IsStatus(err, http.StatusNotFound, http.StatusInternalServerError):
		return "", ErrNotFound
	case err != nil:
		return "", fmt.Errorf("resolve share link: %w", err)
	case share.HinfahrtRecon == "":
		return "", ErrNotFound
	}
	return share.HinfahrtRecon, nil
}

// productClasses maps the HAFAS product class ("PC") to bahn.de's product.
var productClasses = map[string]string{
	"0": journey.ProductICE,
	"1": journey.ProductECIC,
	"2": journey.ProductIR,
	"3": journey.ProductRegional,
	"4": journey.ProductSBahn,
	"5": journey.ProductBus,
	"6": journey.ProductShip,
	"7": journey.ProductUBahn,
	"8": journey.ProductTram,
	"9": journey.ProductOnDemand,
}

// DecodeRecon decodes the "recon" string bahn.de hands out for a shared
// connection. It consists of sections introduced by ¶NAME¶:
//
//	HKI  the legs, separated by §, each a $-separated record:
//	     type $ from $ to $ departure $ arrival $ train name $ ...
//	     with type T for a ride, stops as HAFAS location ids
//	     (A=1@O=name@X=lon@Y=lat@L=EVA@) and times as yyyymmddHHMM
//	KCC  base64 text with one "Z#..." line per ride, holding among others
//	     the category (CA), train number (ZE) and product class (PC)
func DecodeRecon(recon string) (journey.Journey, error) {
	sections := map[string]string{}
	parts := strings.Split(recon, "¶")
	for index := 1; index+1 < len(parts); index += 2 {
		sections[parts[index]] = parts[index+1]
	}
	hki := sections["HKI"]
	if hki == "" {
		return journey.Journey{}, errors.New("no legs (HKI section) found")
	}
	details := rideDetails(sections["KCC"])

	var connection journey.Journey
	ride := 0
	for _, rec := range strings.Split(hki, "§") {
		fields := strings.Split(rec, "$")
		if len(fields) < 6 {
			return journey.Journey{}, fmt.Errorf("leg %d: unexpected record %q", len(connection.Legs), rec)
		}
		dep, err := time.ParseInLocation("200601021504", fields[3], journey.Berlin)
		if err != nil {
			return journey.Journey{}, fmt.Errorf("leg %d departure: %w", len(connection.Legs), err)
		}
		arr, err := time.ParseInLocation("200601021504", fields[4], journey.Berlin)
		if err != nil {
			return journey.Journey{}, fmt.Errorf("leg %d arrival: %w", len(connection.Legs), err)
		}
		leg := journey.Leg{
			Index:       len(connection.Legs),
			Walk:        fields[0] != "T",
			Origin:      dblink.ParseLocation(fields[1]),
			Destination: dblink.ParseLocation(fields[2]),
			Departure:   dep,
			Arrival:     arr,
		}
		if !leg.Walk {
			name := strings.Join(strings.Fields(fields[5]), " ") // "IC            185" -> "IC 185"
			if strings.IndexFunc(name, unicode.IsLetter) >= 0 {
				leg.Line = name
			}
			leg.Number = digits(name)
			if ride < len(details) {
				detail := details[ride]
				leg.Category = detail["CA"]
				leg.Product = productClasses[detail["PC"]]
				if number := digits(detail["ZE"]); number != "" {
					leg.Number = number
				}
			}
			if leg.Product == "" {
				leg.Product = productFromName(leg.Line)
			}
			ride++
		}
		connection.Legs = append(connection.Legs, leg)
	}
	connection.Origin = connection.Legs[0].Origin.Name
	connection.Destination = connection.Legs[len(connection.Legs)-1].Destination.Name
	return connection, nil
}

// rideDetails parses the KCC section into one key/value map per ride.
func rideDetails(kcc string) []map[string]string {
	raw, err := base64.StdEncoding.DecodeString(kcc)
	if err != nil {
		return nil
	}
	var out []map[string]string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Z#") {
			continue
		}
		fields := strings.Split(line, "#")
		detail := map[string]string{}
		for index := 1; index+1 < len(fields); index += 2 {
			detail[fields[index]] = fields[index+1]
		}
		out = append(out, detail)
	}
	return out
}

// productFromName is the fallback when the ride details are missing: only
// long-distance trains are recognised, everything else stays unknown.
func productFromName(line string) string {
	switch strings.ToUpper(strings.SplitN(line, " ", 2)[0]) {
	case "ICE":
		return journey.ProductICE
	case "IC", "EC", "ECE", "RJ", "RJX", "TGV", "NJ", "EN":
		return journey.ProductECIC
	}
	return ""
}

// digits returns the last word of text if it is a number, e.g. the train
// number in "IC 185", and "" for names like "S6".
func digits(text string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	last := words[len(words)-1]
	for _, char := range last {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return last
}
