package hafas

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
)

// trip is a TripSearch answer cut down to the fields read, shaped like the
// VBB answer for Berlin Hbf – Potsdam Hbf.
const trip = `{"outConL":[
 {"date":"20261201","secL":[{"type":"JNY","dep":{"dTimeS":"100700"},"arr":{"aTimeS":"104200"}}],
  "trfRes":{"statusCode":"OK","fareSetL":[{"name":"Berlin Tarifgebiet A-B-C: Einzelfahrausweis","desc":"Via: Berlin ABC","fareL":[{"name":"Regeltarif"}]}]}},
 {"date":"20261201","secL":[{"type":"JNY","dep":{"dTimeS":"235000"},"arr":{"aTimeS":"01001000"}}],
  "trfRes":{"statusCode":"OK","fareSetL":[]}},
 {"date":"20261201","secL":[{"type":"WALK"},{"type":"JNY","dep":{"dTimeS":"120000"},"arr":{"aTimeS":"123000"}}],
  "trfRes":{"statusCode":"NA"}}
]}`

func TestExtract(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	var res tripResult
	if err := json.Unmarshal([]byte(trip), &res); err != nil {
		t.Fatal(err)
	}
	var vbb Provider
	for _, provider := range DefaultProviders() {
		if provider.Nets[0].Net == "vbb" {
			vbb = provider
		}
	}
	ride := func(depH, depM, arrDay, arrH, arrM int) journey.Leg {
		return journey.Leg{
			Departure: time.Date(2026, 12, 1, depH, depM, 0, 0, berlin),
			Arrival:   time.Date(2026, 12, arrDay, arrH, arrM, 0, 0, berlin),
		}
	}

	inside, ok := extract(vbb, res, ride(10, 7, 1, 10, 42), berlin)
	if !ok || len(inside.Zones) != 1 || inside.Zones[0].Net != "vbb" || len(inside.KnownNets) != 1 {
		t.Errorf("ride inside the VBB: %+v", inside)
	}
	// No VBB fare for the ride: it leaves the Verbund. The arrival is on the
	// next day ("01001000").
	outside, ok := extract(vbb, res, ride(23, 50, 2, 0, 10), berlin)
	if !ok || len(outside.Zones) != 0 || len(outside.KnownNets) != 1 {
		t.Errorf("ride leaving the VBB: %+v", outside)
	}
	// Fares not computed: nothing is said about the Verbund.
	unknown, ok := extract(vbb, res, ride(12, 0, 1, 12, 30), berlin)
	if !ok || len(unknown.KnownNets) != 0 || len(unknown.Zones) != 0 {
		t.Errorf("ride without fares: %+v", unknown)
	}
	if _, ok := extract(vbb, res, ride(10, 8, 1, 10, 42), berlin); ok {
		t.Error("matched a ride with another departure")
	}
}

// NASA names the zones of MDV and marego fares.
func TestExtractZones(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	var nasa Provider
	for _, provider := range DefaultProviders() {
		if provider.Name == "NASA" {
			nasa = provider
		}
	}
	var res tripResult
	if err := json.Unmarshal([]byte(`{"outConL":[{"date":"20261201","secL":[{"type":"JNY","dep":{"dTimeS":"100000"},"arr":{"aTimeS":"103000"}}],
	  "trfRes":{"statusCode":"OK","fareSetL":[{"name":"Tarifzone(n): 110 162 163 225 210 Preisstufe: 5 Zonen","desc":"MDV Tarifverbund"}]}}]}`), &res); err != nil {
		t.Fatal(err)
	}
	leg := journey.Leg{Departure: time.Date(2026, 12, 1, 10, 0, 0, 0, berlin), Arrival: time.Date(2026, 12, 1, 10, 30, 0, 0, berlin)}
	assessment, ok := extract(nasa, res, leg, berlin)
	if !ok || len(assessment.Zones) != 1 || assessment.Zones[0].Net != "mdv" || strings.Join(assessment.Zones[0].Zones, " ") != "110 162 163 225 210" {
		t.Errorf("assessment = %+v", assessment)
	}
	if len(assessment.KnownNets) != 0 {
		t.Errorf("NASA is not verified to sell no fare outside: %v", assessment.KnownNets)
	}
}
