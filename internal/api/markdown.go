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
	check.Valid:       "Deine Tickets decken die ganze Verbindung ab.",
	check.Partial:     "Deine Tickets decken die Verbindung nur teilweise ab.",
	check.Invalid:     "Keines deiner Tickets gilt auf dieser Verbindung.",
	check.Unknown:     "Ob deine Tickets auf dieser Verbindung gelten, ließ sich nicht klären.",
	check.NotRequired: "Für diese Verbindung ist kein Ticket nötig.",
}

var weekdays = [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}

// clock formats a time of day in German time.
func clock(moment time.Time) string { return moment.In(journey.Berlin).Format("15:04") }

// Markdown renders the response as a short German Markdown document that
// reads well both in a terminal and for an agent.
func (response Response) Markdown() string {
	view := response.View()
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s\n\n", view.Title)
	if view.When != "" {
		fmt.Fprintf(&builder, "%s\n\n", view.When)
	}
	if view.Note != "" {
		fmt.Fprintf(&builder, "%s\n\n", view.Note)
	}
	fmt.Fprintf(&builder, "**%s**\n\n", view.Summary)

	builder.WriteString("## Verbindung\n\n")
	for legIndex, leg := range view.Legs {
		fmt.Fprintf(&builder, "%d. %s: %s %s → %s %s\n", legIndex+1, leg.Name, leg.From, leg.Departure, leg.To, leg.Arrival)
		if leg.LocalLine != "" {
			fmt.Fprintf(&builder, "   - fährt auch als %s (Nahverkehr)\n", leg.LocalLine)
		}
		for _, stretch := range leg.Stretches {
			covered := "**kein Ticket**"
			if len(stretch.Tickets) > 0 {
				covered = strings.Join(stretch.Tickets, "; ")
			}
			fmt.Fprintf(&builder, "   - %s: %s\n", stretch.Name, covered)
		}
		if len(leg.Unknown) > 0 {
			fmt.Fprintf(&builder, "   - nicht prüfbar: %s\n", strings.Join(leg.Unknown, "; "))
		}
		if leg.Zones != "" {
			fmt.Fprintf(&builder, "   - Zonen: %s\n", leg.Zones)
		}
	}
	if len(view.Missing) > 0 {
		builder.WriteString("\n## Noch ohne Ticket\n\n")
		for _, stretch := range view.Missing {
			fmt.Fprintf(&builder, "- %s\n", stretch)
		}
	}

	builder.WriteString("\n## Tickets\n\n")
	for _, ticket := range view.Tickets {
		fmt.Fprintf(&builder, "- **%s**: %s\n", ticket.Label, ticket.VerdictText)
	}
	for _, ticket := range view.Tickets {
		fmt.Fprintf(&builder, "\n## %s: %s\n\n", ticket.Label, ticket.VerdictText)
		for _, leg := range ticket.Legs {
			fmt.Fprintf(&builder, "- %s: **%s**. %s\n", leg.Name, leg.VerdictText, leg.Reason)
		}
	}
	return builder.String()
}

// TicketLabel names a ticket for display.
func TicketLabel(ticket check.Ticket) string {
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
