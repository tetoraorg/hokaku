package cpu

import (
	"context"
	"errors"
	"math"

	gopsutilcpu "github.com/shirou/gopsutil/v4/cpu"
)

type Counters struct {
	User      float64 `json:"user"`
	System    float64 `json:"system"`
	Idle      float64 `json:"idle"`
	Wait      float64 `json:"wait"`
	Nice      float64 `json:"nice"`
	Interrupt float64 `json:"interrupt"`
	SoftIRQ   float64 `json:"softirq"`
	Steal     float64 `json:"steal"`
}

func Read(ctx context.Context) (Counters, error) {
	times, err := gopsutilcpu.TimesWithContext(ctx, false)
	if err != nil {
		return Counters{}, err
	}
	return countersFromTimes(times)
}

// countersFromTimes maps gopsutil's aggregate counters to the wire fields. Linux's
// User and Nice counters already include guest time, so Guest and GuestNice
// must not be added again.
func countersFromTimes(times []gopsutilcpu.TimesStat) (Counters, error) {
	if len(times) != 1 {
		return Counters{}, errors.New("missing aggregate CPU counters")
	}
	c := times[0]
	counters := Counters{
		User:      c.User,
		System:    c.System,
		Idle:      c.Idle,
		Wait:      c.Iowait,
		Nice:      c.Nice,
		Interrupt: c.Irq,
		SoftIRQ:   c.Softirq,
		Steal:     c.Steal,
	}
	var total float64
	for _, value := range [...]float64{
		counters.User,
		counters.System,
		counters.Idle,
		counters.Wait,
		counters.Nice,
		counters.Interrupt,
		counters.SoftIRQ,
		counters.Steal,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return Counters{}, errors.New("invalid CPU counter")
		}
		total += value
	}
	if math.IsNaN(total) || math.IsInf(total, 0) {
		return Counters{}, errors.New("invalid CPU counter total")
	}
	return counters, nil
}
