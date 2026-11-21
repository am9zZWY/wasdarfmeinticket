package api

import (
	"fmt"

	"github.com/am9zZWY/wasdarfmeinticket/internal/check"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

// View is a response prepared for display. All wording is decided here, so
// that the Markdown rendering and a user interface say the same.
type View struct {
	// Title names the connection: "Tübingen Hbf → Horb".
	Title string `json:"title"`
	// When is the day and the time span: "Mo 23.11.2026, 10:45–11:10".
	When string `json:"when,omitempty"`
	Note string `json:"note,omitempty"`
	// Verdict and Summary answer for all tickets together.
	Verdict check.Verdict `json:"verdict"`
	Summary string        `json:"summary"`
	Legs    []LegView     `json:"legs"`
	// Missing lists the stretches no ticket covers.
	Missing []string     `json:"missing,omitempty"`
	Tickets []TicketView `json:"tickets"`
}

// LegView is one leg of the connection with what the tickets achieve on it.
type LegView struct {
	Name      string `json:"name"`
	Walk      bool   `json:"walk"`
	From      string `json:"from"`
	Departure string `json:"departure"`
	To        string `json:"to"`
	Arrival   string `json:"arrival"`
	// LocalLine is set for a long-distance train that also runs as local
	// transport under this line.
	LocalLine string        `json:"localLine,omitempty"`
	Verdict   check.Verdict `json:"verdict,omitempty"`
	Stretches []StretchView `json:"stretches,omitempty"`
	// Unknown names the tickets that could not be judged on this leg.
	Unknown []string `json:"unknown,omitempty"`
	Zones   string   `json:"zones,omitempty"`
}

// StretchView is a part of a leg and the tickets valid on it; without
// tickets it is a gap.
type StretchView struct {
	Name    string   `json:"name"`
	Tickets []string `json:"tickets"`
}

// TicketView is the outcome for one ticket.
type TicketView struct {
	Label       string          `json:"label"`
	Verdict     check.Verdict   `json:"verdict"`
	VerdictText string          `json:"verdictText"`
	Legs        []TicketLegView `json:"legs"`
}

// TicketLegView is the outcome for one ticket on one leg.
type TicketLegView struct {
	Name        string        `json:"name"`
	Verdict     check.Verdict `json:"verdict"`
	VerdictText string        `json:"verdictText"`
	Reason      string        `json:"reason"`
}

// View prepares the response for display.
func (response Response) View() View {
	view := View{
		Title:   response.Journey.Origin + " → " + response.Journey.Destination,
		Note:    response.Note,
		Verdict: response.Verdict,
		Summary: summaryText[response.Verdict],
	}
	legs := response.Journey.Legs
	if len(legs) > 0 {
		departure := legs[0].Departure.In(journey.Berlin)
		view.When = fmt.Sprintf("%s %s, %s–%s", weekdays[departure.Weekday()], departure.Format("02.01.2006"), clock(departure), clock(legs[len(legs)-1].Arrival))
	}
	for legIndex, leg := range legs {
		legView, missing := response.legView(legIndex, leg)
		view.Legs = append(view.Legs, legView)
		view.Missing = append(view.Missing, missing...)
	}
	for _, result := range response.Results {
		ticket := TicketView{Label: TicketLabel(result.Ticket), Verdict: result.Verdict, VerdictText: verdictText[result.Verdict]}
		for legIndex, legResult := range result.Legs {
			name := fmt.Sprintf("Abschnitt %d", legIndex+1)
			if legIndex < len(legs) {
				name = legs[legIndex].String()
			}
			ticket.Legs = append(ticket.Legs, TicketLegView{Name: name, Verdict: legResult.Verdict, VerdictText: verdictText[legResult.Verdict], Reason: legResult.Reason})
		}
		view.Tickets = append(view.Tickets, ticket)
	}
	return view
}

// legView prepares one leg: its local twin, which tickets cover which
// stretch, and its zones. It also returns the stretches no ticket covers.
func (response Response) legView(legIndex int, leg journey.Leg) (view LegView, missing []string) {
	view = LegView{
		Name: leg.Name(), Walk: leg.Walk,
		From: leg.Origin.Name, Departure: clock(leg.Departure),
		To: leg.Destination.Name, Arrival: clock(leg.Arrival),
	}
	var assessment tariff.Assessment
	if legIndex < len(response.Tariff) {
		assessment = response.Tariff[legIndex]
	}
	if leg.LongDistance() {
		view.LocalLine = assessment.LocalLine
	}
	view.Zones = zoneLine(assessment)
	if legIndex >= len(response.Coverage) {
		return view, nil
	}
	coverage := response.Coverage[legIndex]
	view.Verdict = coverage.Verdict
	view.Unknown = response.ticketLabels(coverage.Unknown)
	for _, span := range coverage.Spans {
		stretch := StretchView{Name: span.From + " → " + span.To, Tickets: response.ticketLabels(span.Tickets)}
		if len(stretch.Tickets) == 0 {
			missing = append(missing, leg.Name()+": "+stretch.Name)
		}
		if len(coverage.Spans) == 1 {
			stretch.Name = "ganze Fahrt"
		}
		view.Stretches = append(view.Stretches, stretch)
	}
	return view, missing
}

// ticketLabels names the tickets with the given indexes.
func (response Response) ticketLabels(indexes []int) []string {
	labels := make([]string, 0, len(indexes))
	for _, index := range indexes {
		if index < len(response.Results) {
			labels = append(labels, TicketLabel(response.Results[index].Ticket))
		}
	}
	return labels
}
