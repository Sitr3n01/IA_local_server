package trayui

import "testing"

func policySnapshot() Snapshot {
	return Snapshot{
		EdgeReachable:   true,
		ProviderReady:   true,
		UpstreamReady:   true,
		StatusAvailable: true,
		SelectedModel:   "gemma",
		MaxActive:       1,
		MaxQueue:        4,
		CapacityOK:      true,
		Models: []Model{{
			ID: "gemma", Available: true,
		}},
	}
}

func TestEvaluateActionsSeparatesSelectedAndLoadedState(t *testing.T) {
	snapshot := policySnapshot()
	policy := EvaluateActions(snapshot, false)
	if !policy.Load || policy.Switch || policy.Unload {
		t.Fatalf("lazy state policy = %+v", policy)
	}
	if !policy.Shutdown {
		t.Fatalf("shutdown policy = %+v", policy)
	}

	snapshot.ActiveModel = "gemma"
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
}

func TestEvaluateActionsRequiresSecondQualifiedModelForSwitch(t *testing.T) {
	snapshot := policySnapshot()
	snapshot.ActiveModel = "gemma"
	snapshot.SelectedModel = "qwen"
	snapshot.Models = append(snapshot.Models, Model{ID: "qwen", Available: false})
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
	if policy.Shutdown || policy.Load || policy.Select || policy.StartServer {
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
	if !policy.StartServer || policy.Load || policy.Unload {
		t.Fatalf("offline policy = %+v", policy)
	}
}

func TestEvaluateActionsClaudeLocalFollowsTheGatewayNotTheQueue(t *testing.T) {
	snapshot := policySnapshot()
	snapshot.ClaudeAvailable = true
	policy := EvaluateActions(snapshot, false)
	if policy.ClaudeLocal || !policy.ClaudeOpen {
		t.Fatalf("gateway down: %+v", policy)
	}
	snapshot.ClaudeGatewayOK = true
	if policy := EvaluateActions(snapshot, false); !policy.ClaudeLocal || !policy.ClaudeOpen {
		t.Fatalf("gateway up: %+v", policy)
	}
	// Opening the local instance stops nothing, so a busy queue must not
	// hold it back the way the old restart-based switch had to.
	snapshot.Active, snapshot.Queued = 1, 3
	if policy := EvaluateActions(snapshot, false); !policy.ClaudeLocal || !policy.ClaudeOpen {
		t.Fatalf("queued requests blocked opening Claude: %+v", policy)
	}
	if policy := EvaluateActions(snapshot, true); policy.ClaudeLocal || policy.ClaudeOpen {
		t.Fatalf("Claude actions offered while another action runs: %+v", policy)
	}
	snapshot.ClaudeAvailable = false
	if policy := EvaluateActions(snapshot, false); policy.ClaudeOpen || policy.ClaudeLocal {
		t.Fatalf("Claude actions without Claude Desktop: %+v", policy)
	}
}
