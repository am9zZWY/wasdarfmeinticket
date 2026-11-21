package check

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

// The RB74 Rottenburg -> Horb as the BW planner reports it: naldo ("tub")
// up to Eyach, the Freudenstadt Verbund ("fds") from Mühlen on.
var rb74 = []string{"Rottenburg", "Bad Niedernau", "Bieringen", "Eyach", "Mühlen", "Horb"}

func rb74Leg(names []string) journey.Leg {
	start := time.Date(2026, 11, 20, 16, 45, 0, 0, berlin)
	leg := journey.Leg{Product: journey.ProductRegional, Number: "32515", Departure: start,
		Arrival: start.Add(time.Duration(len(names)-1) * 5 * time.Minute)}
	for index, name := range names {
		when := start.Add(time.Duration(index) * 5 * time.Minute)
		leg.Stops = append(leg.Stops, journey.Stop{Name: name, Arrival: when, Departure: when})
	}
	leg.Origin, leg.Destination = leg.Stops[0], leg.Stops[len(names)-1]
	return leg
}

// rb74Assess answers for any partial ride in either direction and counts calls.
func rb74Assess(calls *int) AssessFunc {
	naldoZones := map[string]string{"Rottenburg": "12", "Bad Niedernau": "112", "Bieringen": "112", "Eyach": "17"}
	return func(_ context.Context, leg journey.Leg) tariff.Assessment {
		*calls++
		assessment := tariff.Assessment{Matched: true, Source: "EFA-BW", KnownNets: []string{"tub", "fds"}}
		var zones []string
		for _, stop := range leg.Stops {
			zone, ok := naldoZones[stop.Name]
			if !ok {
				return assessment // leaves naldo: no naldo zones for the ride
			}
			if !slices.Contains(zones, zone) {
				zones = append(zones, zone)
			}
		}
		assessment.Zones = []tariff.ZoneSet{{Net: "tub", Zones: zones}}
		return assessment
	}
}

func refine(t *testing.T, ticket Ticket, leg journey.Leg) (Result, int) {
	t.Helper()
	calls := 0
	assess := rb74Assess(&calls)
	connection := journey.Journey{Legs: []journey.Leg{leg}}
	assessments := []tariff.Assessment{assess(context.Background(), leg)}
	calls = 0
	result := Check(ticket, connection, assessments)
	if result.Verdict != Invalid {
		t.Fatalf("whole leg should be invalid before refining: %+v", result)
	}
	result.RefineVerbund(context.Background(), connection, assessments, assess)
	return result, calls
}

func TestRefineVerbund(t *testing.T) {
	zone := func(zones ...string) Ticket { return Ticket{Type: TypeVerbund, Net: "naldo", Zones: zones} }
	reversed := slices.Clone(rb74)
	slices.Reverse(reversed)

	tests := []struct {
		name     string
		ticket   Ticket
		stops    []string
		want     Verdict
		from, to string
		reason   []string
	}{
		{"zones elsewhere in naldo", zone("11", "220"), rb74, Invalid, "", "",
			[]string{"von Rottenburg bis Eyach", "Zonen 12, 112, 17", "fehlen 12, 112, 17"}},
		{"all zones up to the border", zone("12", "112", "17"), rb74, Partial, "Rottenburg", "Eyach",
			[]string{"Eyach bis Horb"}},
		{"zones for the first stops only", zone("12", "112"), rb74, Partial, "Rottenburg", "Bieringen", nil},
		{"network ticket", Ticket{Type: TypeVerbund, Net: "naldo", Network: true}, rb74, Partial, "Rottenburg", "Eyach", nil},
		{"entering naldo", Ticket{Type: TypeVerbund, Net: "naldo", Network: true}, reversed, Partial, "Eyach", "Rottenburg",
			[]string{"Horb bis Eyach"}},
		{"entering naldo with zones for the last stops", zone("12", "112"), reversed, Partial, "Bieringen", "Rottenburg", nil},
		{"entering naldo without matching zones", zone("11"), reversed, Invalid, "", "",
			[]string{"von Eyach bis Rottenburg"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result, calls := refine(t, testCase.ticket, rb74Leg(testCase.stops))
			legResult := result.Legs[0]
			if legResult.Verdict != testCase.want || result.Verdict != testCase.want || legResult.ValidFrom != testCase.from || legResult.ValidTo != testCase.to {
				t.Errorf("got %s %q..%q (%s), want %s %q..%q", legResult.Verdict, legResult.ValidFrom, legResult.ValidTo, legResult.Reason, testCase.want, testCase.from, testCase.to)
			}
			for _, fragment := range testCase.reason {
				if !strings.Contains(legResult.Reason, fragment) {
					t.Errorf("reason %q lacks %q", legResult.Reason, fragment)
				}
			}
			if calls > 12 {
				t.Errorf("%d planner calls for a 6-stop leg", calls)
			}
		})
	}
}

func TestRefineVerbundLeavesOthersAlone(t *testing.T) {
	calls := 0
	assess := rb74Assess(&calls)
	leg := rb74Leg(rb74)
	connection := journey.Journey{Legs: []journey.Leg{leg}}
	assessments := []tariff.Assessment{assess(context.Background(), leg)}
	calls = 0

	deutschlandResult := Check(Ticket{Type: TypeDeutschlandticket}, connection, assessments)
	deutschlandResult.RefineVerbund(context.Background(), connection, assessments, assess)
	if calls != 0 || deutschlandResult.Verdict != Valid {
		t.Errorf("non-verbund ticket was refined: %+v (%d calls)", deutschlandResult, calls)
	}

	// Entirely outside the Verbund: stays invalid, with a clearer reason.
	outside := rb74Leg([]string{"Mühlen", "Horb", "Sulz"})
	outsideJourney := journey.Journey{Legs: []journey.Leg{outside}}
	outsideAssessments := []tariff.Assessment{assess(context.Background(), outside)}
	networkTicket := Ticket{Type: TypeVerbund, Net: "naldo", Network: true}
	after := Check(networkTicket, outsideJourney, outsideAssessments)
	after.RefineVerbund(context.Background(), outsideJourney, outsideAssessments, assess)
	if legResult := after.Legs[0]; after.Verdict != Invalid || legResult.Verdict != Invalid || legResult.ValidTo != "" ||
		!strings.Contains(legResult.Reason, "weder der Anfang (ab Mühlen) noch das Ende (bis Sulz) dieser Fahrt liegt im Verbund naldo") {
		t.Errorf("leg outside the Verbund: %+v", after)
	}

	// Without stop times a partial ride cannot be assessed.
	noTimes := rb74Leg(rb74)
	for index := range noTimes.Stops {
		noTimes.Stops[index].Arrival, noTimes.Stops[index].Departure = time.Time{}, time.Time{}
	}
	noTimesJourney := journey.Journey{Legs: []journey.Leg{noTimes}}
	result := Check(networkTicket, noTimesJourney, assessments)
	result.RefineVerbund(context.Background(), noTimesJourney, assessments, assess)
	if result.Verdict != Invalid || result.Legs[0].ValidTo != "" {
		t.Errorf("leg without stop times was refined: %+v", result)
	}
}
