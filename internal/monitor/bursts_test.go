package monitor

import (
	"math"
	"testing"
	"time"
)

func working(id string, util float64, tps *float64) inferenceSource {
	return inferenceSource{
		ID: id, Label: "LM Studio / Bionic", Activity: activityGenerating, ActivityBasis: "gpu",
		GPUUtil: ptr(util), TokensPerSecond: tps, Models: []sourceModel{{ID: "gemma"}},
	}
}

func idle(id string) inferenceSource {
	return inferenceSource{ID: id, Label: "LM Studio / Bionic", Activity: activityIdle}
}

func TestBurstsRecordAStretchOfActivityWithItsPeaksAndEnergy(t *testing.T) {
	var tracker burstTracker
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	step := func(sources []inferenceSource, power float64) {
		clock = clock.Add(time.Second)
		tracker.observe(clock, sources, ptr(power))
	}
	tracker.observe(clock, []inferenceSource{idle("a")}, ptr(70))
	step([]inferenceSource{working("a", 60, nil)}, 150)
	step([]inferenceSource{working("a", 95, ptr(40))}, 280)
	step([]inferenceSource{working("a", 80, ptr(50))}, 260)
	// Idle again, but not for long enough to call it over.
	step([]inferenceSource{idle("a")}, 70)
	if len(tracker.records()) != 0 {
		t.Fatalf("a burst closed before the quiet period: %+v", tracker.records())
	}
	step([]inferenceSource{idle("a")}, 70)
	step([]inferenceSource{idle("a")}, 70)
	step([]inferenceSource{idle("a")}, 70)

	records := tracker.records()
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	r := records[0]
	if r.SourceID != "a" || r.Label != "LM Studio / Bionic" || r.Model != "gemma" || r.Basis != "gpu" {
		t.Fatalf("record = %+v", r)
	}
	if r.PeakGPUUtil == nil || *r.PeakGPUUtil != 95 || r.PeakPowerW == nil || *r.PeakPowerW != 280 {
		t.Fatalf("peaks = %v %v", r.PeakGPUUtil, r.PeakPowerW)
	}
	if r.TokensPerSecond == nil || *r.TokensPerSecond != 45 {
		t.Fatalf("mean rate = %v, want 45", r.TokensPerSecond)
	}
	// Three active samples (150, 280, 260 W) one second each, the first of which
	// starts the burst and so has no interval before it: (280+260) W over 2 s.
	wantWh := (280.0 + 260.0) / 3600
	if r.EnergyWh == nil || math.Abs(*r.EnergyWh-wantWh) > 1e-9 {
		t.Fatalf("energy = %v, want %.6f", r.EnergyWh, wantWh)
	}
	if r.DurationMS < 3000 || r.DurationMS > 4000 {
		t.Fatalf("duration = %d ms", r.DurationMS)
	}
}

func TestBurstsIgnoreABlipAndSeparateSources(t *testing.T) {
	var tracker burstTracker
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	// A source that is busy for a single sample, and another that stays busy.
	tracker.observe(clock, []inferenceSource{working("blip", 30, nil), working("long", 90, nil)}, nil)
	for range 6 {
		clock = clock.Add(time.Second)
		tracker.observe(clock, []inferenceSource{idle("blip"), working("long", 90, nil)}, nil)
	}
	clock = clock.Add(time.Second)
	// "blip" lasted one sample plus the sample it was seen in: exactly the
	// minimum, so it is kept; a source still working is not closed.
	records := tracker.records()
	if len(records) != 1 || records[0].SourceID != "blip" {
		t.Fatalf("records = %+v", records)
	}
	if records[0].EnergyWh != nil || records[0].PeakPowerW != nil {
		t.Fatalf("a burst with no power reading claimed energy: %+v", records[0])
	}
	// The other one ends when its source leaves the list altogether.
	for range 4 {
		clock = clock.Add(time.Second)
		tracker.observe(clock, nil, nil)
	}
	if got := tracker.records(); len(got) != 2 || got[0].SourceID != "long" {
		t.Fatalf("records = %+v; newest first, and a vanished source ends its burst", got)
	}
}

func TestBurstsAreBoundedAndAlwaysAList(t *testing.T) {
	var tracker burstTracker
	if tracker.records() == nil {
		t.Fatal("records() is nil before anything happened")
	}
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for index := 0; index < maxBursts+10; index++ {
		id := "s" + string(rune('a'+index%26))
		for range 2 {
			clock = clock.Add(time.Second)
			tracker.observe(clock, []inferenceSource{working(id, 50, nil)}, nil)
		}
		for range 5 {
			clock = clock.Add(time.Second)
			tracker.observe(clock, nil, nil)
		}
	}
	if len(tracker.records()) != maxBursts {
		t.Fatalf("kept %d records, want %d", len(tracker.records()), maxBursts)
	}
}

func TestEnergyAcrossAPausedMonitorIsNotInventedByBursts(t *testing.T) {
	var tracker burstTracker
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tracker.observe(clock, []inferenceSource{working("a", 50, nil)}, ptr(200))
	clock = clock.Add(time.Hour) // the machine slept
	tracker.observe(clock, []inferenceSource{working("a", 50, nil)}, ptr(200))
	clock = clock.Add(time.Second)
	tracker.observe(clock, []inferenceSource{working("a", 50, nil)}, ptr(200))
	for range 5 {
		clock = clock.Add(time.Second)
		tracker.observe(clock, nil, nil)
	}
	records := tracker.records()
	if len(records) != 1 || records[0].EnergyWh == nil || *records[0].EnergyWh > 0.1 {
		t.Fatalf("record = %+v; an hour asleep must not become an hour of energy", records)
	}
}
