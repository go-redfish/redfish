package redfish

import (
	"errors"
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

func counter(at time.Duration, kwh float64) Reading {
	return Reading{At: t0.Add(at), EnergyKWh: &kwh}
}

func power(at time.Duration, w float64) Reading {
	return Reading{At: t0.Add(at), PowerWatts: &w}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-12*math.Max(1, math.Abs(b)) }

// A constant power integrates to its exact energy: 1000 W for an hour is 1 kWh,
// whatever the sampling.
func TestConstantPowerGivesExactEnergy(t *testing.T) {
	for _, step := range []time.Duration{time.Minute, 15 * time.Minute, 20 * time.Minute, 30 * time.Second} {
		m := Meter{MaxGap: time.Hour}
		var total float64
		for at := time.Duration(0); at <= time.Hour; at += step {
			iv, ok := m.Observe(power(at, 1000))
			if ok {
				if !iv.Valid || iv.Source != SourcePower {
					t.Fatalf("step %v: %+v", step, iv)
				}
				total += iv.KWh
			}
		}
		if !near(total, 1) {
			t.Errorf("step %v: %v kWh, want 1", step, total)
		}
	}
	// The trapezoid: a ramp 0 to 2000 W over an hour is the mean, 1000 W: 1 kWh.
	m := Meter{MaxGap: time.Hour}
	m.Observe(power(0, 0))
	iv, _ := m.Observe(power(time.Hour, 2000))
	if !iv.Valid || !near(iv.KWh, 1) {
		t.Fatalf("ramp: %+v", iv)
	}
}

// A sample that is missing is not interpolated across: the interval that spans
// it is invalid (the caller falls back), and the next one starts afresh.
func TestMissingPowerSampleIsNotInterpolated(t *testing.T) {
	m := Meter{MaxGap: 20 * time.Minute}
	m.Observe(power(0, 1000))
	iv, ok := m.Observe(power(15*time.Minute, 1000))
	if !ok || !iv.Valid || !near(iv.KWh, 0.25) {
		t.Fatalf("first interval: %+v", iv)
	}
	// The 30 minute sample is lost: the next read is 30 minutes after the last.
	iv, ok = m.Observe(power(45*time.Minute, 1000))
	if !ok || iv.Valid || iv.Reason != ReasonGap || iv.KWh != 0 {
		t.Fatalf("across the gap: %+v", iv)
	}
	iv, _ = m.Observe(power(60*time.Minute, 1000))
	if !iv.Valid || !near(iv.KWh, 0.25) {
		t.Fatalf("after the gap: %+v", iv)
	}
	// The default gap is five minutes.
	var d Meter
	d.Observe(power(0, 1000))
	if iv, _ := d.Observe(power(6*time.Minute, 1000)); iv.Valid {
		t.Fatalf("the default gap: %+v", iv)
	}
}

// A counter that goes down (a BMC reboot) is never billed as a negative
// energy: the interval is invalid, and the energy counted since the reboot
// starts the next one.
func TestCounterResetIsNotBilled(t *testing.T) {
	m := Meter{}
	m.Observe(counter(0, 100))
	iv, _ := m.Observe(counter(5*time.Minute, 101))
	if !iv.Valid || iv.Source != SourceCounter || !near(iv.KWh, 1) {
		t.Fatalf("%+v", iv)
	}
	iv, ok := m.Observe(counter(10*time.Minute, 5)) // rebooted: 5 kWh since
	if !ok || iv.Valid || iv.Reason != ReasonReset || iv.KWh != 0 {
		t.Fatalf("reset: %+v", iv)
	}
	iv, _ = m.Observe(counter(15*time.Minute, 6.5))
	if !iv.Valid || !near(iv.KWh, 1.5) {
		t.Fatalf("after the reset: %+v", iv)
	}
	// A counter does not care about a long gap: it is cumulative.
	iv, _ = m.Observe(counter(10*time.Hour, 8))
	if !iv.Valid || !near(iv.KWh, 1.5) {
		t.Fatalf("a long gap: %+v", iv)
	}
}

func TestMeterSourceChangesAndBadReads(t *testing.T) {
	m := Meter{MaxGap: time.Hour}
	if _, ok := m.Observe(Reading{At: t0}); ok { // nothing to start from
		t.Fatal("an empty read made a baseline")
	}
	m.Observe(counter(0, 10))
	// The BMC now reports only a power: the interval across the change is unknown.
	iv, _ := m.Observe(power(5*time.Minute, 500))
	if iv.Valid || iv.Reason != ReasonSource {
		t.Fatalf("%+v", iv)
	}
	iv, _ = m.Observe(power(10*time.Minute, 500))
	if !iv.Valid || iv.Source != SourcePower {
		t.Fatalf("%+v", iv)
	}
	// An empty read keeps the baseline; the clock stepping back is invalid.
	iv, ok := m.Observe(Reading{At: t0.Add(15 * time.Minute)})
	if !ok || iv.Valid || iv.Reason != ReasonNoReading {
		t.Fatalf("%+v", iv)
	}
	if iv, _ := m.Observe(power(5*time.Minute, 500)); iv.Valid || iv.Reason != ReasonClock {
		t.Fatalf("%+v", iv)
	}
	// Garbage is not a sample.
	bad := math.NaN()
	if iv, _ := m.Observe(Reading{At: t0.Add(time.Hour), PowerWatts: &bad}); iv.Valid {
		t.Fatalf("%+v", iv)
	}
	neg := -3.0
	if iv, _ := m.Observe(Reading{At: t0.Add(time.Hour), EnergyKWh: &neg}); iv.Valid {
		t.Fatalf("%+v", iv)
	}
}

func TestRequireCounter(t *testing.T) {
	if _, err := RequireCounter(power(0, 1)); !errors.Is(err, ErrNoCounter) {
		t.Fatal(err)
	}
	if v, err := RequireCounter(counter(0, 7)); err != nil || v != 7 {
		t.Fatal(v, err)
	}
}

// However the reads come, an interval is valid only with a finite, non negative
// energy; an invalid one holds 0.
func FuzzMeter(f *testing.F) {
	f.Add(int64(0), 100.0, int64(60), 101.0, int64(120), 5.0, true)
	f.Add(int64(0), 1000.0, int64(900), 1000.0, int64(100000), 0.0, false)
	f.Fuzz(func(t *testing.T, a int64, x float64, b int64, y float64, c int64, z float64, counters bool) {
		m := Meter{MaxGap: 10 * time.Minute}
		mk := func(at int64, v float64) Reading {
			r := Reading{At: t0.Add(time.Duration(at%1_000_000) * time.Second)}
			if counters {
				r.EnergyKWh = &v
			} else {
				r.PowerWatts = &v
			}
			return r
		}
		for _, r := range []Reading{mk(a, x), mk(b, y), mk(c, z)} {
			iv, ok := m.Observe(r)
			if !ok {
				continue
			}
			if iv.Valid && (math.IsNaN(iv.KWh) || math.IsInf(iv.KWh, 0) || iv.KWh < 0) {
				t.Fatalf("an unusable energy: %+v", iv)
			}
			if !iv.Valid && (iv.KWh != 0 || iv.Reason == "") {
				t.Fatalf("an invalid interval without a reason or with energy: %+v", iv)
			}
		}
	})
}
