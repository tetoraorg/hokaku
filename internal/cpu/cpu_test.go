package cpu

import (
	"math"
	"testing"

	gopsutilcpu "github.com/shirou/gopsutil/v4/cpu"
)

func TestAggregateMapsWireCountersWithoutAddingGuest(t *testing.T) {
	got, err := countersFromTimes([]gopsutilcpu.TimesStat{{
		User:      11,
		System:    12,
		Idle:      13,
		Iowait:    14,
		Nice:      15,
		Irq:       16,
		Softirq:   17,
		Steal:     18,
		Guest:     1000,
		GuestNice: 2000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := Counters{User: 11, System: 12, Idle: 13, Wait: 14, Nice: 15, Interrupt: 16, SoftIRQ: 17, Steal: 18}
	if got != want {
		t.Fatalf("countersFromTimes() = %+v, want %+v", got, want)
	}
}

func TestAggregateRejectsMissingAndAmbiguousCounters(t *testing.T) {
	for _, times := range [][]gopsutilcpu.TimesStat{nil, {{}, {}}} {
		if _, err := countersFromTimes(times); err == nil {
			t.Fatalf("countersFromTimes(%v) accepted non-aggregate input", times)
		}
	}
}

func TestAggregateRejectsInvalidCounters(t *testing.T) {
	for _, test := range []struct {
		name  string
		value float64
		set   func(*gopsutilcpu.TimesStat, float64)
	}{
		{name: "user negative", value: -1, set: func(s *gopsutilcpu.TimesStat, v float64) { s.User = v }},
		{name: "system nan", value: math.NaN(), set: func(s *gopsutilcpu.TimesStat, v float64) { s.System = v }},
		{name: "idle positive infinity", value: math.Inf(1), set: func(s *gopsutilcpu.TimesStat, v float64) { s.Idle = v }},
		{name: "wait negative infinity", value: math.Inf(-1), set: func(s *gopsutilcpu.TimesStat, v float64) { s.Iowait = v }},
		{name: "nice negative", value: -1, set: func(s *gopsutilcpu.TimesStat, v float64) { s.Nice = v }},
		{name: "interrupt negative", value: -1, set: func(s *gopsutilcpu.TimesStat, v float64) { s.Irq = v }},
		{name: "softirq negative", value: -1, set: func(s *gopsutilcpu.TimesStat, v float64) { s.Softirq = v }},
		{name: "steal negative", value: -1, set: func(s *gopsutilcpu.TimesStat, v float64) { s.Steal = v }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input gopsutilcpu.TimesStat
			test.set(&input, test.value)
			if _, err := countersFromTimes([]gopsutilcpu.TimesStat{input}); err == nil {
				t.Fatal("accepted invalid CPU counter")
			}
		})
	}
}

func TestCountersRejectTotalOverflow(t *testing.T) {
	_, err := countersFromTimes([]gopsutilcpu.TimesStat{{User: math.MaxFloat64, Idle: math.MaxFloat64}})
	if err == nil {
		t.Fatal("accepted overflowing CPU total")
	}
}
