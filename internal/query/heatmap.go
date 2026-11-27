package query

import (
	"fmt"
	"math"
	"time"

	"github.com/jimbeveridge/loghorn/internal/schema"
)

// Heatmap is span count by time bucket and log-duration band.
type Heatmap struct {
	Bucket        string      `json:"bucket"` // as Graph.Bucket
	BucketSeconds float64     `json:"bucketSeconds"`
	Starts        []time.Time `json:"starts"` // as Graph.Starts
	Bands         []Band      `json:"bands"`  // ascending
	Counts        [][]int     `json:"counts"` // [timeIndex][bandIndex]
	Max           int         `json:"max"`    // largest cell count
	Total         int         `json:"total"`  // spans in the grid
	Skipped       int         `json:"skipped"`
}

// Band is one duration band in ms: Lo inclusive, Hi exclusive. A band's Lo is
// the previous band's Hi, bit for bit.
type Band struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

// HeatCell narrows a query to one heatmap cell: From <= time < To and
// Lo <= duration_ms < Hi on the raw number. It is not saved with a favorite.
type HeatCell struct {
	From time.Time `json:"from"`         // inclusive
	To   time.Time `json:"to"`           // exclusive
	Lo   *float64  `json:"lo,omitempty"` // ms, inclusive; nil = no lower bound
	Hi   float64   `json:"hi"`           // ms, exclusive
}

func (c *HeatCell) validate() error {
	if !c.To.After(c.From) {
		return fmt.Errorf("cell: to must be after from")
	}
	if c.Lo != nil && !(*c.Lo < c.Hi) {
		return fmt.Errorf("cell: lo must be below hi")
	}
	return nil
}

const (
	bandsPerDecade = 5
	// minBandMs and maxBandMs bound the grid at 1ns and about 30 years, so
	// one wild outlier cannot add hundreds of bands (or an infinite edge).
	// Shorter durations land in the lowest band; longer ones are skipped.
	minBandMs = 1e-6
	maxBandMs = 1e12
)

var durationField = schema.Field{Path: "duration_ms", Type: schema.TFloat}

// decadeAtOrBelow is the largest d with 10^d <= x, for x > 0. It checks
// against math.Pow, the function the band edges use, so float error in
// Log10 never puts an exact decade on the wrong side.
func decadeAtOrBelow(x float64) int {
	d := int(math.Floor(math.Log10(x)))
	for math.Pow(10, float64(d)) > x {
		d--
	}
	for math.Pow(10, float64(d+1)) <= x {
		d++
	}
	return d
}

// bands spans the decade at or below minMs to the first decade above maxMs
// (at least one decade), five bands per decade with edges 10^(k/5) ms. A
// minMs <= 0 means 1ms.
func bands(minMs, maxMs float64) []Band {
	if !(minMs > 0) || math.IsInf(minMs, 0) {
		minMs = 1
	}
	minMs = min(max(minMs, minBandMs), maxBandMs)
	maxMs = min(maxMs, maxBandMs)
	if !(maxMs >= minMs) {
		maxMs = minMs
	}
	lo := decadeAtOrBelow(minMs)
	hi := max(decadeAtOrBelow(maxMs)+1, lo+1)
	edges := make([]float64, 0, (hi-lo)*bandsPerDecade+1)
	for k := lo * bandsPerDecade; k <= hi*bandsPerDecade; k++ {
		edges = append(edges, math.Pow(10, float64(k)/bandsPerDecade))
	}
	out := make([]Band, len(edges)-1)
	for i := range out {
		out[i] = Band{Lo: edges[i], Hi: edges[i+1]}
	}
	return out
}
