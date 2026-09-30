package monitor

import "time"

// The edge writes down every request because every request passes through it.
// A tool that talks to its own model server leaves no such record, so the
// monitor writes down what it can see of it: a stretch of time in which the
// source was working, how hard it drove the GPU, and what the board drew.
//
// It is not a request. There is no prompt, no token count and, unless the
// server reports its slots, no rate. It answers "did something just run, and
// how much" for tools the monitor cannot see inside.
const (
	// burstQuiet is how long a source must stay idle before its activity is
	// considered over, so a pause between two tokens does not split it in two.
	burstQuiet = 3 * time.Second
	// burstMinimum keeps a single noisy sample from becoming a record.
	burstMinimum = 1 * time.Second
	maxBursts    = 20
)

// externalRecord is one finished stretch of activity.
type externalRecord struct {
	SourceID        string   `json:"source_id"`
	Label           string   `json:"label"`
	Model           string   `json:"model,omitempty"`
	StartedAt       string   `json:"started_at"`
	DurationMS      int64    `json:"duration_ms"`
	PeakGPUUtil     *float64 `json:"peak_gpu_util"`
	PeakPowerW      *float64 `json:"peak_power_w"`
	EnergyWh        *float64 `json:"energy_wh"`
	TokensPerSecond *float64 `json:"tokens_per_second"`
	Basis           string   `json:"basis,omitempty"`

	// Measured says how the numbers were obtained: "server-log" when the
	// server's own timing lines gave them, and empty when the record is only an
	// observation of the GPU. The fields below exist only for the first.
	Measured      string             `json:"measured,omitempty"`
	PromptTokens  *int64             `json:"prompt_tokens"`
	CachedTokens  *int64             `json:"cached_tokens"`
	OutputTokens  *int64             `json:"output_tokens"`
	PromptTPS     *float64           `json:"prompt_tokens_per_second"`
	TTFTMS        *int64             `json:"ttft_ms"`
	PromptMS      *float64           `json:"prompt_ms"`
	DecodeMS      *float64           `json:"decode_ms"`
	ContextTokens *int64             `json:"context_tokens"`
	Truncated     bool               `json:"truncated,omitempty"`
	Measurements  recordMeasurements `json:"measurements"`
}

// recordMeasurements mirrors the edge's evidence labels in the monitor API.
// Prompt and cache read from llama.cpp logs are reconstructed from slot state;
// their source is deliberately different from the server's timing counters.
type recordMeasurements struct {
	Prompt     string `json:"prompt"`
	Cache      string `json:"cache"`
	Output     string `json:"output"`
	PromptRate string `json:"prompt_rate"`
	DecodeRate string `json:"decode_rate"`
}

// startTime parses StartedAt; a record that has none sorts as the oldest.
func (r externalRecord) startTime() time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

type burst struct {
	start, lastActive time.Time
	label, model      string
	basis             string
	peakUtil          float64
	peakPower         float64
	hasUtil, hasPower bool
	energyWh          float64
	rateSum           float64
	rateCount         int
}

type burstTracker struct {
	open map[string]*burst
	done []externalRecord
	last time.Time
}

// observe is called once per sample with the sources as last discovered and the
// board's power at that moment. The energy is the whole board's, not the
// source's share: the desktop's baseline is in it, and the page says so.
func (t *burstTracker) observe(now time.Time, sources []inferenceSource, power *float64) {
	if t.open == nil {
		t.open = make(map[string]*burst)
	}
	elapsed := time.Duration(0)
	if !t.last.IsZero() && now.After(t.last) && now.Sub(t.last) <= energyGap {
		elapsed = now.Sub(t.last)
	}
	t.last = now

	for _, source := range sources {
		// A source whose server log is being read gets exact records from it;
		// an estimate from the GPU beside them would be the same request twice.
		if source.Activity != activityGenerating || source.Metering != "" {
			continue
		}
		current := t.open[source.ID]
		opening := current == nil
		if opening {
			current = &burst{start: now, label: source.Label, basis: source.ActivityBasis}
			t.open[source.ID] = current
		}
		current.lastActive = now
		if len(source.Models) > 0 {
			current.model = source.Models[0].ID
		}
		if source.GPUUtil != nil {
			current.hasUtil = true
			if *source.GPUUtil > current.peakUtil {
				current.peakUtil = *source.GPUUtil
			}
		}
		if power != nil {
			current.hasPower = true
			if *power > current.peakPower {
				current.peakPower = *power
			}
			// The interval that ends at the sample which opens a burst was spent
			// before it began, so it is not the burst's energy.
			if !opening {
				current.energyWh += *power * elapsed.Hours()
			}
		}
		if source.TokensPerSecond != nil {
			current.rateSum += *source.TokensPerSecond
			current.rateCount++
		}
	}

	for id, current := range t.open {
		if now.Sub(current.lastActive) < burstQuiet {
			continue
		}
		delete(t.open, id)
		// The activity lasted until it was last seen, plus the sample it was
		// seen in.
		duration := current.lastActive.Sub(current.start) + sampleInterval
		if duration < burstMinimum {
			continue
		}
		record := externalRecord{
			SourceID:   id,
			Label:      current.label,
			Model:      current.model,
			StartedAt:  current.start.UTC().Format(time.RFC3339Nano),
			DurationMS: duration.Milliseconds(),
			Basis:      current.basis,
		}
		if current.hasUtil {
			record.PeakGPUUtil = ptr(current.peakUtil)
		}
		if current.hasPower {
			record.PeakPowerW = ptr(current.peakPower)
			record.EnergyWh = ptr(current.energyWh)
		}
		if current.rateCount > 0 {
			record.TokensPerSecond = ptr(current.rateSum / float64(current.rateCount))
		}
		t.done = append([]externalRecord{record}, t.done...)
		if len(t.done) > maxBursts {
			t.done = t.done[:maxBursts]
		}
	}
}

// records returns the finished stretches, newest first, as a list that is never
// nil.
func (t *burstTracker) records() []externalRecord {
	out := make([]externalRecord, len(t.done))
	copy(out, t.done)
	return out
}
