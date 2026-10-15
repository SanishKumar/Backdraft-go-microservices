package sim

import (
	"fmt"
	"math"
	"strings"
)

// Verdict says what a scenario did to the system.
type Verdict struct {
	// Kind is "steady" or "overloaded" without a fault. With one it is
	// "recovered"; "recovering" if goodput is still down but the backlog is
	// draining; "metastable" if it is not; or "inconclusive".
	Kind string `json:"kind"`
	// DrainS estimates, for "recovering", how much longer the backlog
	// takes to clear.
	DrainS float64 `json:"drainS,omitempty"`
	// Ratios are goodput / offered load.
	BaselineRatio float64 `json:"baselineRatio"`
	FinalRatio    float64 `json:"finalRatio"`
	MinRatio      float64 `json:"minRatio"`
	// RecoveryS is how long after the fault cleared goodput returned to
	// within 90% of baseline, or -1.
	RecoveryS float64 `json:"recoveryS"`
	// Bottleneck is the most saturated service at the end of the run, with
	// the attempts it was receiving and what it can serve.
	Bottleneck         string  `json:"bottleneck,omitempty"`
	BottleneckLoad     float64 `json:"bottleneckLoad,omitempty"`
	BottleneckCapacity float64 `json:"bottleneckCapacity,omitempty"`
	// Saturated lists every service whose workers were nearly all busy at
	// the end: the bottleneck and the callers stuck waiting on it.
	Saturated []string           `json:"saturated,omitempty"`
	PeakQueue map[string]float64 `json:"peakQueue"`
	Summary   string             `json:"summary"`
}

// rolling returns goodput/offered over a trailing window, per step.
func rolling(r *Result, windowS float64) []float64 {
	w := int(math.Max(1, math.Round(windowS/r.StepS)))
	out := make([]float64, len(r.T))
	var g, o float64
	for i := range r.T {
		g += r.Goodput[i]
		o += r.Offered[i]
		if i >= w {
			g -= r.Goodput[i-w]
			o -= r.Offered[i-w]
		}
		if o > 0 {
			out[i] = math.Min(1, g/o)
		} else {
			out[i] = 1
		}
	}
	return out
}

func meanRatio(r *Result, from, to float64) float64 {
	var g, o float64
	for i, t := range r.T {
		if t >= from && t < to {
			g += r.Goodput[i]
			o += r.Offered[i]
		}
	}
	if o == 0 {
		return 1
	}
	return math.Min(1, g/o)
}

func judge(r *Result, sc Scenario, m *Model) Verdict {
	v := Verdict{RecoveryS: -1, PeakQueue: map[string]float64{}}
	d := sc.DurationS
	if r.StoppedAt > 0 {
		d = r.StoppedAt
	}
	for name, s := range r.Services {
		for _, q := range s.Queue {
			v.PeakQueue[name] = math.Max(v.PeakQueue[name], q)
		}
	}
	ratio := rolling(r, 2)
	v.MinRatio = 1
	for i, t := range r.T {
		if t > 2 {
			v.MinRatio = math.Min(v.MinRatio, ratio[i])
		}
	}
	finalFrom := math.Max(d-5, d*0.8)
	v.FinalRatio = meanRatio(r, finalFrom, d+r.StepS)
	v.Bottleneck, v.BottleneckLoad, v.BottleneckCapacity, v.Saturated = bottleneck(r, m, finalFrom)

	if len(sc.Faults) == 0 {
		v.BaselineRatio = meanRatio(r, 2, d+r.StepS)
		v.Kind = "steady"
		v.Summary = fmt.Sprintf("Steady state: %s of requests succeed.", pct(v.FinalRatio))
		if v.FinalRatio < 0.9 || r.Truncated {
			v.Kind = "overloaded"
			v.Summary = fmt.Sprintf("Overloaded: only %s of requests succeed. %s", pct(v.FinalRatio), v.sustainText())
		}
		return v
	}

	faultStart, faultEnd := math.Inf(1), 0.0
	for _, f := range sc.Faults {
		faultStart = math.Min(faultStart, f.StartS)
		faultEnd = math.Max(faultEnd, f.EndS)
	}
	v.BaselineRatio = meanRatio(r, math.Max(1, faultStart-8), faultStart)
	threshold := 0.9 * v.BaselineRatio

	if r.StoppedAt > 0 {
		v.Kind = "metastable"
		v.Summary = fmt.Sprintf("Did not recover: %s after the fault cleared, goodput was near zero and the backlog still growing. %s", secs(d-faultEnd), v.sustainText())
		return v
	}
	if r.Truncated {
		v.Kind = "metastable"
		v.FinalRatio = 0
		v.Summary = fmt.Sprintf("Did not recover: queues grew past %d requests and kept growing after the fault cleared. %s", maxPending, v.sustainText())
		return v
	}

	// Recovery: the first moment after the fault from which goodput stays
	// within 90% of baseline to the end of the run.
	lastBad := -1
	for i, t := range r.T {
		if t >= faultStart && ratio[i] < threshold {
			lastBad = i
		}
	}
	switch {
	case v.FinalRatio >= threshold:
		v.Kind = "recovered"
		v.RecoveryS = 0
		if lastBad >= 0 && lastBad+1 < len(r.T) {
			v.RecoveryS = math.Max(0, r.T[lastBad+1]-faultEnd)
		}
		v.Summary = fmt.Sprintf("Recovered %s after the fault cleared.", secs(v.RecoveryS))
		if v.MinRatio >= threshold {
			v.Summary = "Rode out the fault without losing goodput."
		}
	case d-faultEnd >= 10:
		// Still down. Whether that is a slow recovery or a stable failure
		// depends on whether the backlog is shrinking.
		before, after := totalQueue(r, d-7, d-5), totalQueue(r, d-2, d+r.StepS)
		if after < before*0.95 && before-after > 5 {
			v.Kind = "recovering"
			v.DrainS = after / ((before - after) / 5)
			v.Summary = fmt.Sprintf("Recovering slowly: %s after the fault cleared, %s of requests succeed, but the backlog is draining and should clear in about %s.",
				secs(d-faultEnd), pct(v.FinalRatio), secs(v.DrainS))
			break
		}
		v.Kind = "metastable"
		v.Summary = fmt.Sprintf("Did not recover: %s after the fault cleared, %s of requests succeed (baseline %s). %s",
			secs(d-faultEnd), pct(v.FinalRatio), pct(v.BaselineRatio), v.sustainText())
	default:
		v.Kind = "inconclusive"
		v.Summary = "Not enough time after the fault cleared to tell whether the system recovers."
	}
	return v
}

// totalQueue is the mean of all services' queues over [from, to).
func totalQueue(r *Result, from, to float64) float64 {
	var sum float64
	n := 0
	for i, t := range r.T {
		if t >= from && t < to {
			for _, s := range r.Services {
				sum += s.Queue[i]
			}
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func (v Verdict) sustainText() string {
	if v.Bottleneck == "" {
		return ""
	}
	text := fmt.Sprintf("%s is the most saturated service.", v.Bottleneck)
	if v.BottleneckLoad > v.BottleneckCapacity {
		text = fmt.Sprintf("%s is receiving %.0f attempts/s against roughly %.0f/s of capacity: retries are sustaining the overload.",
			v.Bottleneck, v.BottleneckLoad, v.BottleneckCapacity)
	}
	var victims []string
	for _, s := range v.Saturated {
		if s != v.Bottleneck {
			victims = append(victims, s)
		}
	}
	switch len(victims) {
	case 0:
	case 1:
		text += fmt.Sprintf(" %s is saturated too, its workers stuck waiting on %s.", victims[0], v.Bottleneck)
	default:
		text += fmt.Sprintf(" %s are saturated too, their workers stuck waiting on %s.", strings.Join(victims, ", "), v.Bottleneck)
	}
	return text
}

// bottleneck finds, among services whose workers were nearly all busy
// over [from, end], the one whose arrivals most exceed what its own work
// allows. A caller whose workers are blocked on a slow dependency is
// saturated too, but it is a victim, not the cause.
func bottleneck(r *Result, m *Model, from float64) (name string, load, capacity float64, saturated []string) {
	best := -1.0
	for _, svc := range sortedNames(r.Services) {
		s := r.Services[svc]
		if s.Workers == 0 {
			continue
		}
		var busy, arr float64
		n := 0
		for i, t := range r.T {
			if t >= from {
				busy += s.Busy[i]
				arr += s.Arrivals[i]
				n++
			}
		}
		if n == 0 || busy/float64(n)/float64(s.Workers) < 0.9 {
			continue
		}
		saturated = append(saturated, svc)
		arr /= float64(n)
		c := float64(s.Workers) / meanHold(m.Services[svc])
		if pressure := arr / c; pressure > best {
			best, name, load, capacity = pressure, svc, arr, c
		}
	}
	return
}

// meanHold estimates how long a request holds a worker when healthy:
// its own time plus nothing else, a lower bound that makes capacity an
// upper bound.
func meanHold(s ServiceModel) float64 {
	if len(s.SelfMs) > 0 {
		sum := 0.0
		for _, x := range s.SelfMs {
			sum += x
		}
		return sum / float64(len(s.SelfMs)) / 1000
	}
	return math.Max(s.MeanSelfMs, 0.001) / 1000
}

func pct(x float64) string { return fmt.Sprintf("%.0f%%", 100*x) }

func secs(s float64) string {
	if s < 10 {
		return fmt.Sprintf("%.1fs", s)
	}
	return fmt.Sprintf("%.0fs", s)
}
