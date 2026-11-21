// Package tariff describes what a fare source knows about one leg.
package tariff

import (
	"context"

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
