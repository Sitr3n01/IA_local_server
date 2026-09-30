package trayui

import "testing"

func policySnapshot() Snapshot {
	return Snapshot{
		EdgeReachable:   true,
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
	if !policy.LaunchCodex || !policy.LaunchOpenCode || !policy.Shutdown {
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

func TestEvaluateActionsBlocksShutdownAndLaunchWhileBusy(t *testing.T) {
	policy := EvaluateActions(policySnapshot(), true)
	if policy.Shutdown || policy.Load || policy.Select || policy.LaunchCodex || policy.LaunchOpenCode || policy.StartServer {
		t.Fatalf("busy policy enabled unsafe action: %+v", policy)
	}
	if !policy.OpenPanel {
		t.Fatal("watching the monitor must stay possible during a long load")
	}
}

func TestEvaluateActionsOffersStartOnlyWhenTheEdgeIsGone(t *testing.T) {
	snapshot := policySnapshot()
	if EvaluateActions(snapshot, false).StartServer {
		t.Fatal("start offered while the edge answers")
	}
	// An edge that answers "not ready" is running: starting it again would
	// do nothing, so the button must not appear.
	snapshot.ProviderReady, snapshot.UpstreamReady, snapshot.StatusAvailable = false, false, false
	if EvaluateActions(snapshot, false).StartServer {
		t.Fatal("start offered for a reachable but unready edge")
	}
	snapshot.EdgeReachable = false
	policy := EvaluateActions(snapshot, false)
	if !policy.StartServer || policy.Load || policy.Unload || policy.LaunchCodex {
		t.Fatalf("offline policy = %+v", policy)
	}
}

func TestEvaluateActionsClaudeModeFollowsGatewayAndQueue(t *testing.T) {
	snapshot := policySnapshot()
	snapshot.ClaudeAvailable = true
	snapshot.ClaudeMode = ClaudeModeAnthropic
	policy := EvaluateActions(snapshot, false)
	if policy.ClaudeAnthropic || policy.ClaudeLocal || !policy.ClaudeOpen {
		t.Fatalf("gateway down: %+v", policy)
	}
	snapshot.ClaudeGatewayOK = true
	if policy := EvaluateActions(snapshot, false); !policy.ClaudeLocal || policy.ClaudeAnthropic {
		t.Fatalf("gateway up: %+v", policy)
	}
	snapshot.Queued = 1
	if policy := EvaluateActions(snapshot, false); policy.ClaudeLocal {
		t.Fatalf("mode switch allowed with a queued request: %+v", policy)
	}
	snapshot.ClaudeAvailable = false
	if policy := EvaluateActions(snapshot, false); policy.ClaudeOpen || policy.ClaudeLocal {
		t.Fatalf("Claude actions without Claude Desktop: %+v", policy)
	}
}
