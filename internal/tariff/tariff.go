// Package tariff describes what a fare source knows about one leg.
package tariff

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
)

// ZoneSet lists the zones of one Verbund (net) a leg passes through.
type ZoneSet struct {
	Net   string   `json:"net"`
	Zones []string `json:"zones"`
	// ZonesUnited is the same set in the Verbund's merged notation, if it has one.
	ZonesUnited []string `json:"zonesUnited,omitempty"`
}

// Product is a ticket product the source offers for the leg.
type Product struct {
	Net  string `json:"net"`
	Name string `json:"name"`
}

// Assessment is the tariff information for one leg.
type Assessment struct {
	// Matched is false when no source could identify the exact ride. All
	// other fields are then empty and checks must answer "unknown".
	Matched bool `json:"matched"`
	// Source names the planner that answered.
	Source string `json:"source,omitempty"`
	// KnownNets are the Verbund codes the answering planner is known to
	// report zones for. Absence of zones for one of them is meaningful.
	KnownNets []string `json:"knownNets,omitempty"`
	// LocalLine is the line name under which the source runs this ride as
	// local transport, e.g. "RE 87" for an IC that also carries a regional
	// designation. Empty when the source knows it as long-distance only.
	LocalLine string    `json:"localLine,omitempty"`
	Zones     []ZoneSet `json:"zones,omitempty"`
	Products  []Product `json:"products,omitempty"`
	// Note explains a missing match, or lists planners that failed while
	// another one answered.
	Note string `json:"note,omitempty"`
}

// Source assesses single legs. Implementations: the online EFA planners
// now, an offline zone database later.
type Source interface {
	Assess(ctx context.Context, leg journey.Leg) Assessment
}

// NotCovered is the note of a source none of whose planners covers the leg.
const NotCovered = "no tariff planner covers this leg"

// Sources asks several sources at once and merges their answers.
type Sources []Source

// Assess implements Source.
func (sources Sources) Assess(ctx context.Context, leg journey.Leg) Assessment {
	answers := make([]Assessment, len(sources))
	var waitGroup sync.WaitGroup
	for index, source := range sources {
		waitGroup.Go(func() { answers[index] = source.Assess(ctx, leg) })
	}
	waitGroup.Wait()
	return Merge(answers)
}

// Merge combines answers for the same leg. A source that does not cover
// the leg is left out of the note unless no source covers it.
func Merge(answers []Assessment) Assessment {
	var out Assessment
	var sources, notes []string
	for _, answer := range answers {
		if answer.Note != "" && answer.Note != NotCovered {
			notes = append(notes, answer.Note)
		}
		if !answer.Matched {
			continue
		}
		out.Matched = true
		sources = append(sources, answer.Source)
		if out.LocalLine == "" {
			out.LocalLine = answer.LocalLine
		}
		out.KnownNets = append(out.KnownNets, answer.KnownNets...)
		out.Zones = append(out.Zones, answer.Zones...)
		for _, product := range answer.Products {
			if !slices.Contains(out.Products, product) {
				out.Products = append(out.Products, product)
			}
		}
	}
	if len(notes) == 0 && !out.Matched {
		notes = []string{NotCovered}
	}
	out.Source = strings.Join(sources, " + ")
	out.Note = strings.Join(notes, "; ")
	return out
}

// BBox is a latitude/longitude rectangle.
type BBox struct {
	MinLat float64 `json:"minLat"`
	MaxLat float64 `json:"maxLat"`
	MinLon float64 `json:"minLon"`
	MaxLon float64 `json:"maxLon"`
}

// Covers reports whether both ends of the leg lie in the rectangle.
func (box BBox) Covers(leg journey.Leg) bool {
	inside := func(stop journey.Stop) bool {
		return stop.Lat >= box.MinLat && stop.Lat <= box.MaxLat && stop.Lon >= box.MinLon && stop.Lon <= box.MaxLon
	}
	return inside(leg.Origin) && inside(leg.Destination)
}
