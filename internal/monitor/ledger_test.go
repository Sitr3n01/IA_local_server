package monitor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCentralRequestFeedKeepsEvidenceAndExcludesGPUBursts(t *testing.T) {
	count, cached := 80, 60
	edge := &edgeInference{Recent: []edgeRecord{{
		StartedAt: "2026-09-29T17:00:00Z", Model: "local", Route: "/v1/chat/completions",
		Status: 200, PromptTokens: &count, CachedTokens: &cached,
		Measurements: recordMeasurements{Prompt: "runtime-usage", Cache: "runtime-usage"},
	}}}
	external := []externalRecord{
		{SourceID: "bionic-1", Label: "Bionic", StartedAt: "2026-09-29T17:01:00Z", Measured: "server-log",
			PromptTokens: int64Ptr(100), CachedTokens: int64Ptr(90),
			Measurements: recordMeasurements{Prompt: "server-log-derived", Cache: "server-log-derived"}},
		{SourceID: "ollama-1", Label: "Ollama", StartedAt: "2026-09-29T17:02:00Z", Basis: "gpu"},
	}
	got := combineMeasuredRequests(edge, external)
	if len(got) != 2 || got[0].Via != "server-log" || got[1].Via != "edge-response" {
		t.Fatalf("feed = %+v", got)
	}
	if got[0].Measurements.Cache != "server-log-derived" || got[1].Measurements.Cache != "runtime-usage" {
		t.Fatalf("provenance = %+v %+v", got[0].Measurements, got[1].Measurements)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "ollama-1") || !strings.Contains(string(encoded), `"output_tokens":null`) {
		t.Fatalf("feed invented a request or omitted unknown counts: %s", encoded)
	}
}
