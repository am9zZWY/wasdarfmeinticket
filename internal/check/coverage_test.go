package check

import (
	"slices"
	"testing"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
)

func stopsLeg(index int, names ...string) journey.Leg {
	leg := journey.Leg{Index: index, Product: journey.ProductRegional}
	for _, name := range names {
		leg.Stops = append(leg.Stops, journey.Stop{Name: name})
	}
	leg.Origin, leg.Destination = leg.Stops[0], leg.Stops[len(names)-1]
	return leg
}

func legs(legResults ...LegResult) Result { return Result{Legs: legResults} }

func TestCoverage(t *testing.T) {
	// Rottenburg - Horb - (walk) - Singen - Konstanz, checked with a naldo
	// ticket (0), a vhb ticket (1) and a ticket nobody could judge (2).
	connection := journey.Journey{Legs: []journey.Leg{
		stopsLeg(0, "Rottenburg", "Bieringen", "Eyach", "Mühlen", "Horb"),
		{Index: 1, Walk: true},
		{Index: 2, Product: journey.ProductRegional, Origin: journey.Stop{Name: "Horb"}, Destination: journey.Stop{Name: "Singen"}},
		stopsLeg(3, "Singen", "Radolfzell", "Konstanz"),
	}}
	walk := LegResult{Verdict: NotRequired}
	results := []Result{
		legs(LegResult{Verdict: Partial, ValidFrom: "Rottenburg", ValidTo: "Eyach"}, walk, LegResult{Verdict: Invalid}, LegResult{Verdict: Invalid}),
		legs(LegResult{Verdict: Invalid}, walk, LegResult{Verdict: Invalid}, LegResult{Verdict: Valid}),
		legs(LegResult{Verdict: Unknown}, walk, LegResult{Verdict: Unknown}, LegResult{Verdict: Unknown}),
	}
	cov := Coverage(connection, results)
	if len(cov) != 4 {
		t.Fatalf("coverage = %+v", cov)
	}

	first := cov[0]
	if first.Verdict != Partial || len(first.Spans) != 2 ||
		first.Spans[0].From != "Rottenburg" || first.Spans[0].To != "Eyach" || !slices.Equal(first.Spans[0].Tickets, []int{0}) ||
		first.Spans[1].From != "Eyach" || first.Spans[1].To != "Horb" || len(first.Spans[1].Tickets) != 0 {
		t.Errorf("first leg = %+v", first)
	}
	if uncovered := gaps(first); len(uncovered) != 1 || uncovered[0].From != "Eyach" || uncovered[0].To != "Horb" {
		t.Errorf("gaps = %+v", uncovered)
	}
	if !slices.Equal(first.Unknown, []int{2}) {
		t.Errorf("unknown = %v", first.Unknown)
	}
	if cov[1].Verdict != NotRequired || len(cov[1].Spans) != 0 {
		t.Errorf("walk = %+v", cov[1])
	}
	// No stop list: one stretch. Nothing covered, one ticket unjudged.
	if coverage := cov[2]; coverage.Verdict != Unknown || len(coverage.Spans) != 1 || coverage.Spans[0].From != "Horb" || coverage.Spans[0].To != "Singen" {
		t.Errorf("leg without stops = %+v", coverage)
	}
	if coverage := cov[3]; coverage.Verdict != Valid || len(coverage.Spans) != 1 || coverage.Spans[0].From != "Singen" || coverage.Spans[0].To != "Konstanz" ||
		!slices.Equal(coverage.Spans[0].Tickets, []int{1}) || len(gaps(coverage)) != 0 {
		t.Errorf("last leg = %+v", coverage)
	}
	if verdict := CoverageVerdict(cov); verdict != Partial {
		t.Errorf("overall = %s", verdict)
	}
}

func TestCoverageCombinesTickets(t *testing.T) {
	connection := journey.Journey{Legs: []journey.Leg{stopsLeg(0, "A", "B", "C", "D")}}
	// One ticket from the start to C, another from B to the end: together
	// they cover the leg, and both are valid between B and C.
	cov := Coverage(connection, []Result{
		legs(LegResult{Verdict: Partial, ValidFrom: "A", ValidTo: "C"}),
		legs(LegResult{Verdict: Partial, ValidFrom: "B", ValidTo: "D"}),
	})
	coverage := cov[0]
	if coverage.Verdict != Valid || len(coverage.Spans) != 3 || !slices.Equal(coverage.Spans[1].Tickets, []int{0, 1}) ||
		coverage.Spans[1].From != "B" || coverage.Spans[1].To != "C" || CoverageVerdict(cov) != Valid {
		t.Errorf("combined = %+v", coverage)
	}
	// A partial result naming stops the leg does not have covers nothing.
	cov = Coverage(connection, []Result{legs(LegResult{Verdict: Partial, ValidFrom: "X", ValidTo: "C"})})
	if cov[0].Verdict != Invalid || len(gaps(cov[0])) != 1 || CoverageVerdict(cov) != Invalid {
		t.Errorf("unknown stop names = %+v", cov[0])
	}
	// One leg covered, the other not judgeable: partial, not "unknown".
	two := journey.Journey{Legs: []journey.Leg{stopsLeg(0, "A", "B"), stopsLeg(1, "B", "C")}}
	cov = Coverage(two, []Result{legs(LegResult{Verdict: Valid}, LegResult{Verdict: Unknown})})
	if cov[1].Verdict != Unknown || CoverageVerdict(cov) != Partial {
		t.Errorf("covered + unknown = %+v, overall %s", cov, CoverageVerdict(cov))
	}
	cov = Coverage(two, []Result{legs(LegResult{Verdict: Unknown}, LegResult{Verdict: Unknown})})
	if CoverageVerdict(cov) != Unknown {
		t.Errorf("all unknown: overall %s", CoverageVerdict(cov))
	}
	// No tickets at all.
	if cov = Coverage(connection, nil); cov[0].Verdict != Invalid {
		t.Errorf("no tickets = %+v", cov[0])
	}
}

// gaps returns the stretches no ticket covers.
func gaps(coverage LegCoverage) []Span {
	var out []Span
	for _, span := range coverage.Spans {
		if len(span.Tickets) == 0 {
			out = append(out, span)
		}
	}
	return out
}
