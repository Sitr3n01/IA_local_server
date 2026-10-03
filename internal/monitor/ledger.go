package monitor

import (
	"sort"
	"time"
)

// meteredRequest is the monitor's bounded, source-neutral request feed. GPU
// activity estimates are excluded: they do not establish a request or tokens.
// The source-specific records remain in the snapshot for existing clients.
type meteredRequest struct {
	SourceID        string             `json:"source_id"`
	SourceLabel     string             `json:"source_label"`
	Via             string             `json:"via"`
	StartedAt       string             `json:"started_at"`
	DurationMS      int64              `json:"duration_ms"`
	Model           string             `json:"model"`
	Route           string             `json:"route,omitempty"`
	Status          *int               `json:"status"`
	Finish          string             `json:"finish,omitempty"`
	PromptTokens    *int64             `json:"prompt_tokens"`
	CachedTokens    *int64             `json:"cached_tokens"`
	OutputTokens    *int64             `json:"output_tokens"`
	PromptTPS       *float64           `json:"prompt_tokens_per_second"`
	DecodeTPS       *float64           `json:"decode_tokens_per_second"`
	TTFTMS          *int64             `json:"ttft_ms"`
	ContextTokens   *int64             `json:"context_tokens"`
	OutputEstimated bool               `json:"output_estimated"`
	Measurements    recordMeasurements `json:"measurements"`
}

const maxMeteredRequests = 64

func count64(value *int) *int64 {
	if value == nil {
		return nil
	}
	n := int64(*value)
	return &n
}

func combineMeasuredRequests(inference *edgeInference, external []externalRecord) []meteredRequest {
	records := make([]meteredRequest, 0, maxMeteredRequests)
	if inference != nil {
		for _, record := range inference.Recent {
			status := record.Status
			records = append(records, meteredRequest{
				SourceID: "cia-edge", SourceLabel: "cia-edge", Via: "edge-response",
				StartedAt: record.StartedAt, DurationMS: record.DurationMS, Model: record.Model,
				Route: record.Route, Status: &status, Finish: record.Finish,
				PromptTokens: count64(record.PromptTokens), CachedTokens: count64(record.CachedTokens),
				OutputTokens: count64(record.OutputTokens), PromptTPS: record.PromptPerSecond,
				DecodeTPS: record.DecodePerSecond, TTFTMS: record.TTFTMS,
				ContextTokens: count64(record.ContextTokens), OutputEstimated: record.OutputEstimated,
				Measurements: record.Measurements,
			})
		}
	}
	for _, record := range external {
		if record.Measured != "server-log" {
			continue
		}
		records = append(records, meteredRequest{
			SourceID: record.SourceID, SourceLabel: record.Label, Via: "server-log",
			StartedAt: record.StartedAt, DurationMS: record.DurationMS, Model: record.Model,
			PromptTokens: record.PromptTokens, CachedTokens: record.CachedTokens,
			OutputTokens: record.OutputTokens, PromptTPS: record.PromptTPS,
			DecodeTPS: record.TokensPerSecond, TTFTMS: record.TTFTMS,
			ContextTokens: record.ContextTokens, Measurements: record.Measurements,
		})
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, records[i].StartedAt)
		b, _ := time.Parse(time.RFC3339Nano, records[j].StartedAt)
		return a.After(b)
	})
	if len(records) > maxMeteredRequests {
		records = records[:maxMeteredRequests]
	}
	return records
}
