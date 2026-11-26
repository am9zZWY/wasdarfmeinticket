// Package hafas asks HAFAS journey planners (mgate interface) for the fares
// of a single ride. They sell Verbund fares but name no zones in a common
// form, so an answer only tells whether a ride lies inside the Verbund and
// which tickets are sold for it.
package hafas

import (
	"context"
	"crypto/md5"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/httpx"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/progress"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

//go:embed providers.json
var providersJSON []byte

// maxStopDistance is how far a planner stop may be from the DB stop to be
// considered the same place.
const maxStopDistance = 1500.0 // metres

// tariffTTL is how long an answer is reused.
const tariffTTL = 7 * 24 * time.Hour

// Provider is one HAFAS installation. Client, Auth, Ext, Ver and the salts
// are what the provider's own apps send (see public-transport/transport-apis).
type Provider struct {
	Name     string          `json:"name"`
	Endpoint string          `json:"endpoint"`
	Client   json.RawMessage `json:"client"`
	Auth     json.RawMessage `json:"auth"`
	Ext      string          `json:"ext,omitempty"`
	Ver      string          `json:"ver"`
	// MicMacSalt, hex, signs the request where the provider asks for it.
	MicMacSalt string `json:"micMacSalt,omitempty"`
	// Nets are the Verbünde whose fares the provider sells, each with a
	// pattern that recognises its fares among the fare set names and
	// descriptions. Other tariffs (state tariff, Deutschlandticket) do not
	// count as "inside the Verbund".
	Nets []Net `json:"nets"`
	// ZonePattern, if set, captures the space-separated zones of a fare set
	// in its first group.
	ZonePattern string `json:"zonePattern,omitempty"`
	// NoFareMeansOutside is set where it was verified that the provider
	// sells no Verbund fare for a ride leaving the Verbund. Elsewhere a
	// missing fare is no evidence (NAH.SH sells none in Hamburg, where the
	// SH-Tarif network ticket is valid).
	NoFareMeansOutside bool        `json:"noFareMeansOutside,omitempty"`
	BBox               tariff.BBox `json:"bbox"`
	// Browser is set when the provider allows cross-origin requests.
	Browser bool `json:"browser"`
	zones   *regexp.Regexp
}

// Net is one Verbund of a provider.
type Net struct {
	Net     string `json:"net"`
	Pattern string `json:"pattern"`
	pattern *regexp.Regexp
}

// DefaultProviders returns the built-in provider list.
func DefaultProviders() []Provider {
	var providers []Provider
	if err := json.Unmarshal(providersJSON, &providers); err != nil {
		panic("hafas: invalid embedded providers.json: " + err.Error())
	}
	for index := range providers {
		provider := &providers[index]
		for netIndex := range provider.Nets {
			provider.Nets[netIndex].pattern = regexp.MustCompile(provider.Nets[netIndex].Pattern)
		}
		if provider.ZonePattern != "" {
			provider.zones = regexp.MustCompile(provider.ZonePattern)
		}
	}
	return providers
}

// Engine implements tariff.Source on top of a list of providers.
type Engine struct {
	Providers []Provider
	HTTP      *httpx.Client
	Location  *time.Location
	Cache     *cache.Cache

	mutex sync.Mutex
	stops map[string]string // provider + DB stop -> provider location id
}

// New returns an engine using the built-in providers.
func New(loc *time.Location) *Engine {
	return &Engine{Providers: DefaultProviders(), HTTP: httpx.New(), Location: loc}
}

// Assess asks every provider covering the leg and merges their answers.
func (engine *Engine) Assess(ctx context.Context, leg journey.Leg) tariff.Assessment {
	// ponytail: matched by times only, without the local twin of a
	// long-distance train, so an IC/EC is left to the other sources.
	if !leg.Origin.HasCoord() || !leg.Destination.HasCoord() || leg.LongDistance() {
		return tariff.Assessment{Note: tariff.NotCovered}
	}
	var answers []tariff.Assessment
	var mutex sync.Mutex
	var waitGroup sync.WaitGroup
	for _, provider := range engine.Providers {
		if !provider.BBox.Covers(leg) {
			continue
		}
		waitGroup.Go(func() {
			answer, err := engine.assess(ctx, provider, leg)
			if err != nil {
				progress.Logf(ctx, "%s: %s: keine Auskunft (%v)", provider.Name, leg, err)
				answer = tariff.Assessment{Note: provider.Name + ": " + err.Error()}
			}
			mutex.Lock()
			answers = append(answers, answer)
			mutex.Unlock()
		})
	}
	waitGroup.Wait()
	return tariff.Merge(answers)
}

func (engine *Engine) assess(ctx context.Context, provider Provider, leg journey.Leg) (tariff.Assessment, error) {
	key := "hafas|" + provider.Name + "|" + leg.Line + "|" + leg.Origin.Name + "|" + leg.Destination.Name
	cacheable := leg.Line != ""
	var assessment tariff.Assessment
	if cacheable && engine.Cache.Get(key, &assessment) {
		return assessment, nil
	}
	progress.Logf(ctx, "%s: Tarif für %s abfragen", provider.Name, leg)
	from, err := engine.stopID(ctx, provider, leg.Origin)
	if err != nil {
		return assessment, err
	}
	to, err := engine.stopID(ctx, provider, leg.Destination)
	if err != nil {
		return assessment, err
	}
	dep := leg.Departure.In(engine.Location)
	var res tripResult
	err = engine.call(ctx, provider, "TripSearch", map[string]any{
		"depLocL": []any{map[string]string{"lid": from}},
		"arrLocL": []any{map[string]string{"lid": to}},
		"outDate": dep.Format("20060102"), "outTime": dep.Format("150405"),
		"numF": 4, "getPasslist": false,
		"trfReq": map[string]any{"jnyCl": 2, "cType": "PK", "tvlrProf": []any{map[string]string{"type": "E"}}},
	}, &res)
	if err != nil {
		return assessment, err
	}
	assessment, ok := extract(provider, res, leg, engine.Location)
	if !ok {
		return assessment, fmt.Errorf("ride %s %s not found in planner results", leg.Line, leg.Number)
	}
	if cacheable {
		engine.Cache.Set(key, assessment, tariffTTL)
	}
	return assessment, nil
}

type tripResult struct {
	OutConL []struct {
		Date string `json:"date"`
		SecL []struct {
			Type string `json:"type"`
			Dep  struct {
				Time string `json:"dTimeS"`
			} `json:"dep"`
			Arr struct {
				Time string `json:"aTimeS"`
			} `json:"arr"`
		} `json:"secL"`
		TrfRes struct {
			StatusCode string `json:"statusCode"`
			FareSetL   []struct {
				Name  string `json:"name"`
				Desc  string `json:"desc"`
				FareL []struct {
					Name    string `json:"name"`
					TicketL []struct {
						Name string `json:"name"`
					} `json:"ticketL"`
				} `json:"fareL"`
			} `json:"fareSetL"`
		} `json:"trfRes"`
	} `json:"outConL"`
}

// extract finds the connection that consists of exactly the given ride and
// reads its fares. The ride is recognised by its planned departure and
// arrival only: train numbers differ between HAFAS and the timetable feed
// (S7 7074 there is 28289 here).
func extract(provider Provider, res tripResult, leg journey.Leg, loc *time.Location) (tariff.Assessment, bool) {
	for _, connection := range res.OutConL {
		ride := -1
		for index, section := range connection.SecL {
			if section.Type == "JNY" {
				if ride >= 0 {
					ride = -2
					break
				}
				ride = index
			}
		}
		if ride < 0 {
			continue
		}
		section := connection.SecL[ride]
		dep, depOK := parseTime(connection.Date, section.Dep.Time, loc)
		arr, arrOK := parseTime(connection.Date, section.Arr.Time, loc)
		if !depOK || !arrOK || !dep.Equal(leg.Departure) || !arr.Equal(leg.Arrival) {
			continue
		}
		assessment := tariff.Assessment{Matched: true, Source: provider.Name}
		fares := connection.TrfRes
		if fares.StatusCode != "OK" {
			// Fares not computed: say nothing about the Verbund.
			return assessment, true
		}
		if provider.NoFareMeansOutside {
			for _, net := range provider.Nets {
				assessment.KnownNets = append(assessment.KnownNets, net.Net)
			}
		}
		for _, fareSet := range fares.FareSetL {
			text := fareSet.Name + " " + fareSet.Desc
			net := ""
			for _, rule := range provider.Nets {
				if rule.pattern.MatchString(text) {
					net = rule.Net
					break
				}
			}
			add := func(name string) {
				if product := (tariff.Product{Net: net, Name: name}); name != "" && !slices.Contains(assessment.Products, product) {
					assessment.Products = append(assessment.Products, product)
				}
			}
			add(fareSet.Name)
			for _, fare := range fareSet.FareL {
				add(fare.Name)
				for _, ticket := range fare.TicketL {
					add(ticket.Name)
				}
			}
			if net == "" || slices.ContainsFunc(assessment.Zones, func(zoneSet tariff.ZoneSet) bool { return zoneSet.Net == net }) {
				continue
			}
			// Without a zone pattern only "inside the Verbund" is known;
			// zone tickets then stay unknown.
			zoneSet := tariff.ZoneSet{Net: net}
			if provider.zones != nil {
				if match := provider.zones.FindStringSubmatch(text); match != nil {
					zoneSet.Zones = strings.Fields(match[1])
				}
			}
			assessment.Zones = append(assessment.Zones, zoneSet)
		}
		return assessment, true
	}
	return tariff.Assessment{}, false
}

// parseTime reads a HAFAS time, "HHMMSS" or "DDHHMMSS" with a day offset,
// on the connection's date.
func parseTime(date, clock string, loc *time.Location) (time.Time, bool) {
	day, err := time.ParseInLocation("20060102", date, loc)
	if err != nil || (len(clock) != 6 && len(clock) != 8) {
		return time.Time{}, false
	}
	offset := 0
	if len(clock) == 8 {
		offset, _ = strconv.Atoi(clock[:2])
		clock = clock[2:]
	}
	hour, _ := strconv.Atoi(clock[0:2])
	minute, _ := strconv.Atoi(clock[2:4])
	second, _ := strconv.Atoi(clock[4:6])
	return time.Date(day.Year(), day.Month(), day.Day()+offset, hour, minute, second, 0, loc), true
}

// stopID maps a DB stop to the provider's location id: the candidate by
// name that lies closest to the DB position.
func (engine *Engine) stopID(ctx context.Context, provider Provider, stop journey.Stop) (string, error) {
	key := provider.Name + "|" + stop.ID + "|" + stop.Name
	engine.mutex.Lock()
	id, ok := engine.stops[key]
	engine.mutex.Unlock()
	if ok {
		return id, nil
	}
	var res struct {
		Match struct {
			LocL []struct {
				LID string `json:"lid"`
				Crd struct {
					X int `json:"x"`
					Y int `json:"y"`
				} `json:"crd"`
			} `json:"locL"`
		} `json:"match"`
	}
	if err := engine.call(ctx, provider, "LocMatch", map[string]any{
		"input": map[string]any{"loc": map[string]string{"type": "S", "name": stop.Name}, "maxLoc": 10, "field": "S"},
	}, &res); err != nil {
		return "", err
	}
	best, bestDist := "", math.Inf(1)
	for _, location := range res.Match.LocL {
		candidate := journey.Stop{Lat: float64(location.Crd.Y) / 1e6, Lon: float64(location.Crd.X) / 1e6}
		if distance := stop.DistanceTo(candidate); distance < bestDist {
			best, bestDist = location.LID, distance
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

// call performs one mgate request and decodes its result into out.
func (engine *Engine) call(ctx context.Context, provider Provider, method string, request, out any) error {
	body := map[string]any{
		"lang": "de", "client": provider.Client, "auth": provider.Auth, "ver": provider.Ver,
		"svcReqL": []any{map[string]any{"meth": method, "req": request}},
	}
	if provider.Ext != "" {
		body["ext"] = provider.Ext
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := provider.Endpoint
	if provider.MicMacSalt != "" {
		salt, err := hex.DecodeString(provider.MicMacSalt)
		if err != nil {
			return err
		}
		mic := md5hex(data)
		url += "?mic=" + mic + "&mac=" + md5hex(append([]byte(mic), salt...))
	}
	var resp struct {
		Err     string `json:"err"`
		SvcResL []struct {
			Err    string          `json:"err"`
			ErrTxt string          `json:"errTxt"`
			Res    json.RawMessage `json:"res"`
		} `json:"svcResL"`
	}
	if err := engine.HTTP.PostJSON(ctx, url, data, &resp); err != nil {
		return err
	}
	if resp.Err != "" && resp.Err != "OK" {
		return fmt.Errorf("%s: %s", method, resp.Err)
	}
	if len(resp.SvcResL) == 0 {
		return fmt.Errorf("%s: empty answer", method)
	}
	if result := resp.SvcResL[0]; result.Err != "OK" {
		return fmt.Errorf("%s: %s %s", method, result.Err, result.ErrTxt)
	}
	return json.Unmarshal(resp.SvcResL[0].Res, out)
}

func md5hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}
