package check

import (
	"context"
	"strings"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

// AssessFunc returns the tariff information for a (partial) leg.
type AssessFunc func(ctx context.Context, leg journey.Leg) tariff.Assessment

// RefineVerbund looks closer at legs a Verbund zone ticket does not cover as
// a whole. By assessing the ride between intermediate stops it finds how far
// the ticket is valid ("partial"), or explains which zones the Verbund part
// of the leg would need. Other results are left untouched.
func (result *Result) RefineVerbund(ctx context.Context, connection journey.Journey, assessments []tariff.Assessment, assess AssessFunc) {
	if result.Ticket.Type != TypeVerbund {
		return
	}
	for index := range result.Legs {
		if index >= len(connection.Legs) || index >= len(assessments) {
			break
		}
		if result.Legs[index].Verdict != Invalid || !assessments[index].Matched {
			continue
		}
		if legResult, ok := refineLeg(ctx, result.Ticket, connection.Legs[index], assess); ok {
			result.Legs[index] = legResult
		}
	}
	result.Verdict = overall(result.Legs)
}

func refineLeg(ctx context.Context, ticket Ticket, leg journey.Leg, assess AssessFunc) (LegResult, bool) {
	stopCount := len(leg.Stops)
	if stopCount < 3 {
		return LegResult{}, false
	}
	net := canonicalNet(ticket.Net)
	assessPart := func(from, to int) (tariff.Assessment, bool) {
		sub, ok := leg.Sub(from, to)
		if !ok {
			return tariff.Assessment{}, false
		}
		return assess(ctx, sub), true
	}
	// The vehicle is the same on every part of the leg, so the leg itself
	// stands for the part when the ticket is judged.
	ticketValid := func(from, to int) bool {
		assessment, ok := assessPart(from, to)
		if !ok {
			return false
		}
		verdict, _ := verbund(ticket, leg, assessment)
		return verdict == Valid
	}
	inNet := func(from, to int) bool {
		assessment, ok := assessPart(from, to)
		_, has := zonesFor(assessment, net)
		return ok && has
	}
	first, last := leg.Stops[0].Name, leg.Stops[stopCount-1].Name
	legResult := LegResult{Leg: leg.Index}

	// How far is the ticket itself valid, from the start or towards the end?
	if stopIndex := prefixEnd(stopCount, ticketValid); stopIndex > 0 {
		legResult.Verdict, legResult.ValidFrom, legResult.ValidTo = Partial, first, leg.Stops[stopIndex].Name
		legResult.Reason = "gültig von " + first + " bis " + legResult.ValidTo + "; für " + legResult.ValidTo + " bis " + last + " ist ein weiteres Ticket nötig"
		return legResult, true
	}
	if stopIndex := suffixStart(stopCount, ticketValid); stopIndex < stopCount-1 {
		legResult.Verdict, legResult.ValidFrom, legResult.ValidTo = Partial, leg.Stops[stopIndex].Name, last
		legResult.Reason = "gültig erst von " + legResult.ValidFrom + " bis " + last + "; für " + first + " bis " + legResult.ValidFrom + " ist ein weiteres Ticket nötig"
		return legResult, true
	}

	// Not valid anywhere: say where the Verbund lies and what it would need.
	legResult.Verdict = Invalid
	if stopIndex := prefixEnd(stopCount, inNet); stopIndex > 0 {
		assessment, _ := assessPart(0, stopIndex)
		legResult.Reason = extentReason(ticket, net, assessment, first, leg.Stops[stopIndex].Name)
		return legResult, true
	}
	if stopIndex := suffixStart(stopCount, inNet); stopIndex < stopCount-1 {
		assessment, _ := assessPart(stopIndex, stopCount-1)
		legResult.Reason = extentReason(ticket, net, assessment, leg.Stops[stopIndex].Name, last)
		return legResult, true
	}
	// Neither end touches the Verbund. Only say so if the ends could be
	// assessed at all.
	head, okHead := assessPart(0, 1)
	tail, okTail := assessPart(stopCount-2, stopCount-1)
	if okHead && okTail && head.Matched && tail.Matched {
		// Worded about the ride, not the stations: a station can belong
		// to the Verbund while the ride to or from it crosses its border.
		legResult.Reason = "weder der Anfang (ab " + first + ") noch das Ende (bis " + last + ") dieser Fahrt liegt im Verbund " + ticket.Net + " " + bySource(head)
		return legResult, true
	}
	return LegResult{}, false
}

// prefixEnd returns the largest index of an intermediate stop for which the
// ride from the first stop to it satisfies ok, or 0. It assumes that a shorter
// ride from the same start satisfies ok whenever a longer one does.
func prefixEnd(stopCount int, ok func(from, to int) bool) int {
	best, low, high := 0, 1, stopCount-2
	for low <= high {
		mid := (low + high) / 2
		if ok(0, mid) {
			best, low = mid, mid+1
		} else {
			high = mid - 1
		}
	}
	return best
}

// suffixStart returns the smallest index of an intermediate stop for which
// the ride from it to the last stop satisfies ok, or the last stop's index.
func suffixStart(stopCount int, ok func(from, to int) bool) int {
	best, low, high := stopCount-1, 1, stopCount-2
	for low <= high {
		mid := (low + high) / 2
		if ok(mid, stopCount-1) {
			best, high = mid, mid-1
		} else {
			low = mid + 1
		}
	}
	return best
}

func extentReason(ticket Ticket, net string, assessment tariff.Assessment, from, to string) string {
	zoneSet, _ := zonesFor(assessment, net)
	return "Verbund " + ticket.Net + " reicht auf dieser Fahrt nur von " + from + " bis " + to +
		"; dafür werden die Zonen " + strings.Join(zoneSet.Zones, ", ") + " benötigt, im Ticket fehlen " +
		strings.Join(missingZones(ticket.Zones, zoneSet.Zones), ", ") + " " + bySource(assessment)
}

func zonesFor(assessment tariff.Assessment, net string) (tariff.ZoneSet, bool) {
	for _, zoneSet := range assessment.Zones {
		if zoneSet.Net == net {
			return zoneSet, true
		}
	}
	return tariff.ZoneSet{}, false
}
