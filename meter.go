package redfish

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Source says where an Interval's energy comes from.
type Source string

const (
	// SourceCounter: the difference of two reads of an energy counter
	// (EnergykWh): exact.
	SourceCounter Source = "counter"
	// SourcePower: the integral of the polled power (PowerWatts or
	// PowerConsumedWatts) by the trapezoid rule: an estimate between samples,
	// exact for a constant power.
	SourcePower Source = "power"
)

// Why an Interval holds no energy.
const (
	// ReasonBaseline: the first usable sample; nothing to difference yet.
	ReasonBaseline = "baseline"
	// ReasonReset: the energy counter went down (the BMC rebooted or its counter
	// wrapped): the energy of the interval is unknown, and a negative delta is
	// never reported as consumption.
	ReasonReset = "counter reset"
	// ReasonGap: two power samples are further apart than MaxGap; the power
	// between them is not interpolated.
	ReasonGap = "gap"
	// ReasonSource: the BMC changed what it reports between the two reads (a
	// counter, then only a power, or the reverse).
	ReasonSource = "source changed"
	// ReasonClock: the second read is not after the first.
	ReasonClock = "clock"
	// ReasonNoReading: the read carries no energy and no power.
	ReasonNoReading = "no reading"
)

// Interval is the energy a node used between two successive reads.
type Interval struct {
	Start, End time.Time
	// Valid is true when KWh is the energy of the interval. When false, the
	// energy is unknown, KWh is 0 and Reason says why: the caller should fall
	// back (a calibrated factor, say), never bill 0 as a measurement.
	Valid  bool
	Reason string
	KWh    float64
	Source Source
}

// Meter turns the successive Readings of one BMC into intervals. It is not safe
// for concurrent use.
//
// With an energy counter the interval is the counter's difference: a counter
// is cumulative, so a missed read loses nothing. With only a power, the energy
// is the trapezoid (P0 + P1) / 2 × dt between two samples; samples further
// apart than MaxGap are not joined, since the power in between is unknown.
type Meter struct {
	// MaxGap is the longest time between two power samples that is integrated;
	// 0 means 5 minutes. It does not apply to a counter.
	MaxGap time.Duration

	prev *Reading
}

// DefaultMaxGap is the MaxGap of a zero Meter.
const DefaultMaxGap = 5 * time.Minute

// kWhPerWattSecond converts W·s (J) to kWh.
const kWhPerWattSecond = 1.0 / 3_600_000

// Observe records a read and returns the interval since the previous one.
// ok is false for the first read (the baseline). A read that cannot be used
// returns an invalid Interval, and becomes the new baseline when it carries a
// sample, so the next interval starts there.
func (m *Meter) Observe(r Reading) (iv Interval, ok bool) {
	if !usableReading(r) {
		// Nothing to difference against: keep the old baseline.
		if m.prev == nil {
			return Interval{}, false
		}
		return Interval{Start: m.prev.At, End: r.At, Reason: ReasonNoReading}, true
	}
	p := m.prev
	cur := r
	m.prev = &cur
	if p == nil {
		return Interval{}, false
	}
	iv = Interval{Start: p.At, End: r.At}
	if !r.At.After(p.At) {
		iv.Reason = ReasonClock
		return iv, true
	}
	switch {
	case p.EnergyKWh != nil && r.EnergyKWh != nil:
		d := *r.EnergyKWh - *p.EnergyKWh
		if d < 0 {
			iv.Reason = ReasonReset
			return iv, true
		}
		iv.Valid, iv.KWh, iv.Source = true, d, SourceCounter
	case p.EnergyKWh == nil && r.EnergyKWh == nil && p.PowerWatts != nil && r.PowerWatts != nil:
		gap := m.MaxGap
		if gap <= 0 {
			gap = DefaultMaxGap
		}
		dt := r.At.Sub(p.At)
		if dt > gap {
			iv.Reason = ReasonGap
			return iv, true
		}
		iv.Valid, iv.Source = true, SourcePower
		iv.KWh = (*p.PowerWatts + *r.PowerWatts) / 2 * dt.Seconds() * kWhPerWattSecond
	default:
		iv.Reason = ReasonSource
	}
	return iv, true
}

// usableReading says whether a read can serve as a sample: a finite, non
// negative counter, else a finite, non negative power.
func usableReading(r Reading) bool {
	ok := func(v *float64) bool { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 }
	return ok(r.EnergyKWh) || ok(r.PowerWatts)
}

// ErrNoCounter is returned by RequireCounter when a read has no energy counter.
var ErrNoCounter = errors.New("redfish: the BMC reports no energy counter")

// RequireCounter returns the counter of a read, ErrNoCounter when it has none:
// for a caller that bills only a measured counter.
func RequireCounter(r Reading) (float64, error) {
	if r.EnergyKWh == nil {
		return 0, fmt.Errorf("%w (chassis %s)", ErrNoCounter, r.Chassis)
	}
	return *r.EnergyKWh, nil
}
