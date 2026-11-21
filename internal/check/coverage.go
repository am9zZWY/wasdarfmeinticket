package check

import (
	"slices"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
)

// Span is a stretch of a leg between two stops and the tickets valid on it.
type Span struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Tickets are indexes into the checked tickets; empty for a gap.
	Tickets []int `json:"tickets"`
}

// LegCoverage combines all tickets for one leg.
type LegCoverage struct {
	Leg int `json:"leg"`
	// Verdict is "valid" if tickets cover the whole leg, "partial" if they
	// cover a part, "invalid" if none covers anything, "unknown" if nothing
	// is covered but at least one ticket could not be judged, and
	// "not_required" for walks.
	Verdict Verdict `json:"verdict"`
	// Spans split the leg into consecutive stretches with the same set of
	// valid tickets. A span without tickets is a gap.
	Spans []Span `json:"spans,omitempty"`
	// Unknown lists tickets that could not be judged on this leg.
	Unknown []int `json:"unknown,omitempty"`
}

// Coverage combines the results of all tickets into one view per leg: which
// stretch is covered by which tickets, and where a further ticket is needed.
// results must stem from Check (and RefineVerbund) on the same journey.
func Coverage(connection journey.Journey, results []Result) []LegCoverage {
	out := make([]LegCoverage, len(connection.Legs))
	for legIndex, leg := range connection.Legs {
		out[legIndex] = legCoverage(legIndex, leg, results)
	}
	return out
}

// legCoverage combines what the tickets achieve on one leg.
func legCoverage(legIndex int, leg journey.Leg, results []Result) LegCoverage {
	coverage := LegCoverage{Leg: leg.Index}
	if leg.Walk {
		coverage.Verdict = NotRequired
		return coverage
	}
	// Stops of the leg; without a stop list the leg is one stretch.
	names := []string{leg.Origin.Name, leg.Destination.Name}
	if len(leg.Stops) >= 2 {
		names = names[:0]
		for _, stop := range leg.Stops {
			names = append(names, stop.Name)
		}
	}
	// valid[stretch] lists the tickets valid between one stop and the next.
	valid := make([][]int, len(names)-1)
	for ticketIndex, result := range results {
		if legIndex >= len(result.Legs) {
			continue
		}
		from, to := 0, 0
		switch legResult := result.Legs[legIndex]; legResult.Verdict {
		case Valid:
			to = len(valid)
		case Partial:
			from, to = slices.Index(names, legResult.ValidFrom), slices.Index(names, legResult.ValidTo)
		case Unknown:
			coverage.Unknown = append(coverage.Unknown, ticketIndex)
		}
		for stretch := from; from >= 0 && stretch < to; stretch++ {
			valid[stretch] = append(valid[stretch], ticketIndex)
		}
	}
	// Merge neighbouring stretches with the same tickets.
	covered, gaps := false, false
	for stretch, tickets := range valid {
		if len(tickets) == 0 {
			gaps = true
		} else {
			covered = true
		}
		if spanCount := len(coverage.Spans); spanCount > 0 && slices.Equal(coverage.Spans[spanCount-1].Tickets, tickets) {
			coverage.Spans[spanCount-1].To = names[stretch+1]
			continue
		}
		if tickets == nil {
			tickets = []int{}
		}
		coverage.Spans = append(coverage.Spans, Span{From: names[stretch], To: names[stretch+1], Tickets: tickets})
	}
	switch {
	case !gaps:
		coverage.Verdict = Valid
	case covered:
		coverage.Verdict = Partial
	case len(coverage.Unknown) > 0:
		coverage.Verdict = Unknown
	default:
		coverage.Verdict = Invalid
	}
	return coverage
}

// CoverageVerdict sums up the coverage of the whole journey. If some of it
// is covered and the rest could not be judged, that is "partial": what is
// known for certain is that a part is covered.
func CoverageVerdict(cov []LegCoverage) Verdict {
	legs := make([]LegResult, len(cov))
	covered := false
	for index, coverage := range cov {
		legs[index].Verdict = coverage.Verdict
		covered = covered || coverage.Verdict == Valid || coverage.Verdict == Partial
	}
	verdict := overall(legs)
	if verdict == Unknown && covered {
		return Partial
	}
	return verdict
}
