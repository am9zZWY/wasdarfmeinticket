package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/check"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
)

var verdictText = map[check.Verdict]string{
	check.Valid:       "gültig",
	check.Partial:     "teilweise gültig",
	check.Invalid:     "nicht gültig",
	check.Unknown:     "unbekannt",
	check.NotRequired: "kein Ticket nötig",
}

// summaryText is the one-line answer for all tickets together.
var summaryText = map[check.Verdict]string{
	check.Valid:       "**Deine Tickets decken die ganze Verbindung ab.**",
	check.Partial:     "**Deine Tickets decken die Verbindung nur teilweise ab.**",
	check.Invalid:     "**Keines deiner Tickets gilt auf dieser Verbindung.**",
	check.Unknown:     "**Ob deine Tickets auf dieser Verbindung gelten, ließ sich nicht klären.**",
	check.NotRequired: "**Für diese Verbindung ist kein Ticket nötig.**",
}

// ticketLabels names the tickets with the given indexes.
func (response Response) ticketLabels(idx []int) string {
	labels := make([]string, 0, len(idx))
	for _, index := range idx {
		if index < len(response.Results) {
			labels = append(labels, ticketLabel(response.Results[index].Ticket))
		}
	}
	return strings.Join(labels, "; ")
}

var weekdays = [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}

// clock formats a time of day in German time.
func clock(moment time.Time) string { return moment.In(journey.Berlin).Format("15:04") }

// Markdown renders the response as a short German Markdown document that
// reads well both in a terminal and for an agent.
func (response Response) Markdown() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s → %s\n\n", response.Journey.Origin, response.Journey.Destination)
	if legCount := len(response.Journey.Legs); legCount > 0 {
		dep := response.Journey.Legs[0].Departure.In(journey.Berlin)
		fmt.Fprintf(&builder, "%s %s, %s–%s\n\n", weekdays[dep.Weekday()], dep.Format("02.01.2006"), clock(dep), clock(response.Journey.Legs[legCount-1].Arrival))
	}
	if response.Note != "" {
		fmt.Fprintf(&builder, "%s\n\n", response.Note)
	}
	fmt.Fprintf(&builder, "%s\n\n", summaryText[response.Verdict])

	builder.WriteString("## Verbindung\n\n")
	var needed []string
	for legIndex, leg := range response.Journey.Legs {
		fmt.Fprintf(&builder, "%d. %s: %s %s → %s %s\n", legIndex+1, leg.Name(), leg.Origin.Name, clock(leg.Departure), leg.Destination.Name, clock(leg.Arrival))
		needed = append(needed, response.legDetails(&builder, legIndex, leg)...)
	}
	if len(needed) > 0 {
		builder.WriteString("\n## Noch ohne Ticket\n\n")
		for _, stretch := range needed {
			fmt.Fprintf(&builder, "- %s\n", stretch)
		}
	}

	builder.WriteString("\n## Tickets\n\n")
	for _, res := range response.Results {
		fmt.Fprintf(&builder, "- **%s**: %s\n", ticketLabel(res.Ticket), verdictText[res.Verdict])
	}
	for _, res := range response.Results {
		fmt.Fprintf(&builder, "\n## %s: %s\n\n", ticketLabel(res.Ticket), verdictText[res.Verdict])
		for legIndex, legResult := range res.Legs {
			name := fmt.Sprintf("Abschnitt %d", legIndex+1)
			if legIndex < len(response.Journey.Legs) {
				name = response.Journey.Legs[legIndex].String()
			}
			fmt.Fprintf(&builder, "- %s: **%s**. %s\n", name, verdictText[legResult.Verdict], legResult.Reason)
		}
	}
	return builder.String()
}

// legDetails writes the sub-items of one leg: its local twin, which tickets
// cover which stretch, and its zones. It returns the stretches no ticket
// covers.
func (response Response) legDetails(builder *strings.Builder, legIndex int, leg journey.Leg) (needed []string) {
	var assessment tariff.Assessment
	if legIndex < len(response.Tariff) {
		assessment = response.Tariff[legIndex]
	}
	if assessment.LocalLine != "" && leg.LongDistance() {
		fmt.Fprintf(builder, "   - fährt auch als %s (Nahverkehr)\n", assessment.LocalLine)
	}
	if legIndex < len(response.Coverage) {
		coverage := response.Coverage[legIndex]
		for _, span := range coverage.Spans {
			stretch := span.From + " → " + span.To
			if len(coverage.Spans) == 1 {
				stretch = "ganze Fahrt"
			}
			if len(span.Tickets) == 0 {
				fmt.Fprintf(builder, "   - %s: **kein Ticket**\n", stretch)
				needed = append(needed, leg.Name()+": "+span.From+" → "+span.To)
				continue
			}
			fmt.Fprintf(builder, "   - %s: %s\n", stretch, response.ticketLabels(span.Tickets))
		}
		if len(coverage.Unknown) > 0 {
			fmt.Fprintf(builder, "   - nicht prüfbar: %s\n", response.ticketLabels(coverage.Unknown))
		}
	}
	if zones := zoneLine(assessment); zones != "" {
		fmt.Fprintf(builder, "   - Zonen: %s\n", zones)
	}
	return needed
}

func ticketLabel(ticket check.Ticket) string {
	switch ticket.Type {
	case check.TypeDeutschlandticket:
		return "Deutschlandticket"
	case check.TypeLaenderticket:
		return check.LaenderticketLabel(ticket.Name)
	case check.TypeVerbund:
		label := "Verbundticket"
		if ticket.Net != "" {
			label += " " + ticket.Net
		}
		switch {
		case ticket.Network:
			label += ", ganzes Netz"
		case len(ticket.Zones) > 0:
			label += ", Zonen " + strings.Join(ticket.Zones, ", ")
		}
		switch {
		case ticket.From != "" && ticket.To != "":
			label += " (" + ticket.From + " – " + ticket.To + ")"
		case ticket.Link != "":
			label += " (aus Verbindungslink)"
		}
		return label
	}
	return ticket.Type
}

// zoneLine summarises what the fare source knows about a leg.
func zoneLine(assessment tariff.Assessment) string {
	if !assessment.Matched {
		return ""
	}
	var parts []string
	for _, zoneSet := range assessment.Zones {
		parts = append(parts, check.DisplayNet(zoneSet.Net)+" "+strings.Join(zoneSet.Zones, ", "))
	}
	if len(parts) == 0 {
		parts = []string{"keine Verbundzonen für die ganze Fahrt"}
	}
	return strings.Join(parts, "; ") + " (Tarifauskunft " + assessment.Source + ")"
}
