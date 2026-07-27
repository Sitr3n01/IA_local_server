package trayui

import "testing"

func policySnapshot() Snapshot {
	return Snapshot{
		ProviderReady:   true,
		UpstreamReady:   true,
		StatusAvailable: true,
		SelectedModel:   "local-coding",
		MaxActive:       1,
		MaxQueue:        4,
		CapacityOK:      true,
		Models: []Model{{
			ID: "local-coding", Available: true, Codex: true, OpenCode: true,
		}},
	}
}

func TestEvaluateActionsSeparatesSelectedAndLoadedState(t *testing.T) {
	snapshot := policySnapshot()
	policy := EvaluateActions(snapshot, false)
	if !policy.Load || policy.Switch || policy.Unload {
		t.Fatalf("lazy state policy = %+v", policy)
	}
	if !policy.LaunchCodex || !policy.LaunchOpenCode || !policy.Exit {
		t.Fatalf("launch policy = %+v", policy)
	}

	snapshot.ActiveModel = "local-coding"
	policy = EvaluateActions(snapshot, false)
	if policy.Load || policy.Switch || !policy.Unload {
		t.Fatalf("loaded state policy = %+v", policy)
	}
}

func TestEvaluateActionsFailsClosedWithoutOperationalStatus(t *testing.T) {
	snapshot := policySnapshot()
	snapshot.StatusAvailable = false
	policy := EvaluateActions(snapshot, false)
	if policy.Load || policy.Switch || policy.Unload {
		t.Fatalf("administrative action enabled without status: %+v", policy)
	}
	if !policy.LaunchCodex {
		t.Fatal("healthy data-plane launch should remain available")
	}
}

func TestEvaluateActionsRequiresSecondQualifiedModelForSwitch(t *testing.T) {
	snapshot := policySnapshot()
	snapshot.ActiveModel = "local-coding"
	snapshot.SelectedModel = "local-fast"
	snapshot.Models = append(snapshot.Models, Model{ID: "local-fast", Available: false, Codex: true})
	if policy := EvaluateActions(snapshot, false); policy.Switch {
		t.Fatalf("switch enabled for unavailable candidate: %+v", policy)
	}
	snapshot.Models[1].Available = true
	if policy := EvaluateActions(snapshot, false); !policy.Switch {
		t.Fatalf("switch disabled for two available models: %+v", policy)
	}
}

func TestEvaluateActionsBlocksExitAndLaunchWhileBusy(t *testing.T) {
	policy := EvaluateActions(policySnapshot(), true)
	if policy.Exit || policy.Load || policy.LaunchCodex || policy.LaunchOpenCode {
		t.Fatalf("busy policy enabled unsafe action: %+v", policy)
	}
}
