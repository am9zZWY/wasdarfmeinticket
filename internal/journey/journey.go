// Package journey holds the provider-independent model of a connection.
package journey

import (
	"math"
	"time"
	_ "time/tzdata" // do not depend on the host's zoneinfo
)

// Berlin is the time zone of German timetables: bahn.de gives its times in
// it, and times are shown to the user in it.
var Berlin = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	return loc
}()

// Product classes as reported by bahn.de (produktGattung).
const (
	ProductICE      = "ICE"
	ProductECIC     = "EC_IC"
	ProductIR       = "IR"
	ProductRegional = "REGIONAL"
	ProductSBahn    = "SBAHN"
	ProductBus      = "BUS"
	ProductShip     = "SCHIFF"
	ProductUBahn    = "UBAHN"
	ProductTram     = "TRAM"
	ProductOnDemand = "ANRUFPFLICHTIG"
)

// Journey is one concrete connection, made of consecutive legs.
type Journey struct {
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	Legs        []Leg  `json:"legs"`
}

// Leg is one ride on a single vehicle, or a walk between two stops.
type Leg struct {
	Index       int       `json:"index"`
	Walk        bool      `json:"walk"`
	Product     string    `json:"product,omitempty"`
	Category    string    `json:"category,omitempty"`
	Line        string    `json:"line,omitempty"`
	Number      string    `json:"number,omitempty"`
	Operator    string    `json:"operator,omitempty"`
	Origin      Stop      `json:"origin"`
	Destination Stop      `json:"destination"`
	Departure   time.Time `json:"departure"`
	Arrival     time.Time `json:"arrival"`
	Stops       []Stop    `json:"stops,omitempty"`
}

// Stop is a station or stop. Lat/Lon are zero when unknown.
type Stop struct {
	Name string  `json:"name"`
	ID   string  `json:"id,omitempty"` // EVA number for railway stations
	Lat  float64 `json:"lat,omitempty"`
	Lon  float64 `json:"lon,omitempty"`
	// Planned times at this stop within a leg's stop list; zero if unknown.
	Arrival   time.Time `json:"arrival,omitzero"`
	Departure time.Time `json:"departure,omitzero"`
}

// DistanceTo returns the great-circle distance to another stop in metres.
func (stop Stop) DistanceTo(other Stop) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat := (other.Lat - stop.Lat) * rad
	dLon := (other.Lon - stop.Lon) * rad
	haversine := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(stop.Lat*rad)*math.Cos(other.Lat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(haversine))
}

// HasCoord reports whether the stop carries a usable position.
func (stop Stop) HasCoord() bool { return stop.Lat != 0 || stop.Lon != 0 }

// Sub returns the part of the leg between its stops from and to (from < to), the
// same vehicle boarded and left at intermediate stops. It reports false
// when the stop list or the stop times do not allow that.
func (leg Leg) Sub(from, to int) (Leg, bool) {
	if leg.Walk || from < 0 || to >= len(leg.Stops) || from >= to {
		return Leg{}, false
	}
	part := leg
	part.Origin, part.Destination = leg.Stops[from], leg.Stops[to]
	part.Stops = leg.Stops[from : to+1]
	part.Departure, part.Arrival = leg.Stops[from].Departure, leg.Stops[to].Arrival
	if from == 0 {
		part.Departure = leg.Departure
	}
	if to == len(leg.Stops)-1 {
		part.Arrival = leg.Arrival
	}
	if part.Departure.IsZero() || part.Arrival.IsZero() {
		return Leg{}, false
	}
	return part, true
}

// Name names the vehicle of the leg even if the line is unknown.
func (leg Leg) Name() string {
	switch {
	case leg.Walk:
		return "Fußweg"
	case leg.Line != "":
		return leg.Line
	case leg.Number != "":
		return "Zug " + leg.Number
	}
	return "Fahrt"
}

// String describes the leg for messages: "RB74 Rottenburg → Horb".
func (leg Leg) String() string {
	return leg.Name() + " " + leg.Origin.Name + " → " + leg.Destination.Name
}

// LongDistance reports whether the leg is a long-distance train.
func (leg Leg) LongDistance() bool {
	switch leg.Product {
	case ProductICE, ProductECIC, ProductIR:
		return true
	}
	return false
}
