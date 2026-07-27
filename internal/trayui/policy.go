package trayui

// ActionPolicy is the testable authorization projection for the native menu.
// The edge still validates every mutation; this layer keeps the UI fail-closed
// when its operational snapshot is incomplete or stale.
type ActionPolicy struct {
	Selected        Model
	SelectedOK      bool
	AvailableModels int
	Load            bool
	Switch          bool
	Unload          bool
	LaunchCodex     bool
	LaunchOpenCode  bool
	Exit            bool
}

func EvaluateActions(snapshot Snapshot, busy bool) ActionPolicy {
	selected, selectedOK := findModel(snapshot.Models, snapshot.SelectedModel)
	policy := ActionPolicy{Selected: selected, SelectedOK: selectedOK, Exit: !busy}
	for _, model := range snapshot.Models {
		if model.Available {
			policy.AvailableModels++
		}
	}

	lifecycle := selectedOK && selected.Available && !busy && snapshot.StatusAvailable &&
		snapshot.Active == 0 && snapshot.Queued == 0 && snapshot.UpstreamReady && snapshot.CapacityOK
	policy.Load = lifecycle && snapshot.ActiveModel == ""
	policy.Switch = lifecycle && policy.AvailableModels > 1 && snapshot.ActiveModel != "" && snapshot.ActiveModel != snapshot.SelectedModel
	policy.Unload = !busy && snapshot.StatusAvailable && snapshot.ActiveModel != "" && snapshot.Active == 0 && snapshot.Queued == 0
	policy.LaunchCodex = !busy && snapshot.ProviderReady && selectedOK && selected.Available && selected.Codex
	policy.LaunchOpenCode = !busy && snapshot.ProviderReady && selectedOK && selected.Available && selected.OpenCode
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
