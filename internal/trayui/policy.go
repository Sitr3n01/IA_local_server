package trayui

// ActionPolicy is the testable authorization projection for the flyout. The
// edge still validates every mutation; this layer keeps the UI fail-closed
// when its operational snapshot is incomplete or stale.
type ActionPolicy struct {
	Selected        Model
	SelectedOK      bool
	AvailableModels int
	Select          bool
	Load            bool
	Switch          bool
	Unload          bool
	ClaudeAnthropic bool
	ClaudeLocal     bool
	StartServer     bool
	OpenPanel       bool
	Shutdown        bool
}

// EvaluateActions decides which controls are live. busy is true while one
// lifecycle action runs; the tray runs them one at a time.
func EvaluateActions(snapshot Snapshot, busy bool) ActionPolicy {
	selected, selectedOK := findModel(snapshot.Models, snapshot.SelectedModel)
	policy := ActionPolicy{
		Selected:   selected,
		SelectedOK: selectedOK,
		// Opening the monitor reads state and changes nothing, so a long model
		// load never locks the operator out of watching it.
		OpenPanel: true,
		Shutdown:  !busy,
	}
	for _, model := range snapshot.Models {
		if model.Available {
			policy.AvailableModels++
		}
	}
	idle := snapshot.Active == 0 && snapshot.Queued == 0

	policy.Select = !busy && policy.AvailableModels > 0
	lifecycle := selectedOK && selected.Available && !busy && snapshot.StatusAvailable &&
		idle && snapshot.UpstreamReady && snapshot.CapacityOK
	policy.Load = lifecycle && snapshot.ActiveModel == ""
	policy.Switch = lifecycle && policy.AvailableModels > 1 && snapshot.ActiveModel != "" && snapshot.ActiveModel != snapshot.SelectedModel
	policy.Unload = !busy && snapshot.StatusAvailable && snapshot.ActiveModel != "" && idle

	// Opening Claude Local stops no process and interrupts no request, so it
	// does not wait for the queue to drain.
	policy.ClaudeAnthropic = snapshot.ClaudeAvailable && !busy
	policy.ClaudeLocal = snapshot.ClaudeAvailable && !busy && snapshot.ClaudeGatewayOK

	policy.StartServer = !busy && !snapshot.EdgeReachable
	return policy
}

func findModel(models []Model, id string) (Model, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}
