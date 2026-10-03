package trayui

import (
	"errors"
	"strings"
	"testing"
)

func viewSnapshot() Snapshot {
	snapshot := policySnapshot()
	snapshot.Environment = "canary"
	snapshot.SelectedModel = "qwen"
	snapshot.Models = []Model{
		{ID: "gemma", DisplayName: "Gemma 4 12B QAT | UD-Q4_K_XL | 256k context", Available: true},
		{ID: "qwen", DisplayName: "Qwen 3.8 27B | UD-IQ4_XS | 32k context", Available: true},
		{ID: "hidden", DisplayName: "Not published", Available: false},
	}
	return snapshot
}

func TestBuildViewBeforeTheFirstSnapshotClaimsNothing(t *testing.T) {
	view := BuildView(ViewState{})
	if view.Pill != "Conectando" || view.IconTone != ToneMuted || view.Policy.StartServer {
		t.Fatalf("view before the first snapshot = %+v", view)
	}
}

func TestBuildViewIdleServerKeepsTheBrandIcon(t *testing.T) {
	view := BuildView(ViewState{Snapshot: viewSnapshot(), Loaded: true})
	if view.Pill != "Ocioso" || view.Tone != ToneMuted || view.IconTone != ToneAccent {
		t.Fatalf("idle view = %q %q icon %q", view.Pill, view.Tone, view.IconTone)
	}
	if view.Environment != "CANARY" || view.ModelAction != ModelActionLoad || view.ShowUnload {
		t.Fatalf("idle view = %+v", view)
	}
	if len(view.Models) != 2 {
		t.Fatalf("unpublished model listed: %+v", view.Models)
	}
	if row := view.Models[1]; row.Name != "Qwen 3.8 27B" || row.Detail != "UD-IQ4_XS · 32k context" || !row.Selected || row.Loaded {
		t.Fatalf("row = %+v", row)
	}
}

func TestBuildViewLoadedModelOffersSwitchAndUnload(t *testing.T) {
	snapshot := viewSnapshot()
	snapshot.ActiveModel = "gemma"
	view := BuildView(ViewState{Loaded: true, Snapshot: snapshot})
	if view.Pill != "Pronto" || view.Subtitle != "Gemma 4 12B QAT · UD-Q4_K_XL · 256k context" {
		t.Fatalf("loaded view = %q %q", view.Pill, view.Subtitle)
	}
	if view.ModelAction != ModelActionSwitch || !view.ShowUnload || !view.Policy.Switch {
		t.Fatalf("loaded view actions = %+v", view)
	}
	if !view.Models[0].Loaded || view.Tooltip != "IA Local · Pronto · Gemma 4 12B QAT" {
		t.Fatalf("loaded row %+v tooltip %q", view.Models[0], view.Tooltip)
	}
	snapshot.SelectedModel = "gemma"
	if view := BuildView(ViewState{Loaded: true, Snapshot: snapshot}); view.ModelAction != ModelActionNone {
		t.Fatalf("selected model already loaded but action %v", view.ModelAction)
	}
}

func TestBuildViewOfflineAndStarting(t *testing.T) {
	snapshot := viewSnapshot()
	snapshot.EdgeReachable, snapshot.StatusAvailable, snapshot.ProviderReady, snapshot.UpstreamReady = false, false, false, false
	view := BuildView(ViewState{Loaded: true, Snapshot: snapshot})
	if view.Tone != ToneDanger || view.IconTone != ToneDanger || !view.Policy.StartServer {
		t.Fatalf("offline view = %+v", view)
	}
	view = BuildView(ViewState{Loaded: true, Snapshot: snapshot, Starting: true})
	if view.Pill != "Iniciando" || !view.Progress || view.Tone != ToneInfo || view.Policy.StartServer {
		t.Fatalf("starting view = %+v", view)
	}
}

func TestBuildViewBusyShowsTheActivityAndLocksLifecycle(t *testing.T) {
	view := BuildView(ViewState{Loaded: true, Snapshot: viewSnapshot(), Activity: "Carregando o modelo"})
	if view.Title != "Carregando o modelo" || !view.Progress || view.Policy.Load || view.Policy.Select {
		t.Fatalf("busy view = %+v", view)
	}
	if view.Subtitle != "Qwen 3.8 27B · UD-IQ4_XS · 32k context" {
		t.Fatalf("busy subtitle = %q", view.Subtitle)
	}
}

func TestBuildViewWorkShowsChipsAndCountsWhatShutdownInterrupts(t *testing.T) {
	snapshot := viewSnapshot()
	snapshot.ActiveModel = "qwen"
	snapshot.Active, snapshot.Queued = 1, 2
	view := BuildView(ViewState{Loaded: true, Snapshot: snapshot})
	if view.Pill != "Na fila" || view.InFlight != 3 || len(view.Chips) != 2 {
		t.Fatalf("working view = %+v", view)
	}
	if view.Chips[0].Text != "1 pedido em andamento" || view.Chips[1].Text != "2 na fila" {
		t.Fatalf("chips = %+v", view.Chips)
	}
}

func TestBuildViewNoticesExplainBlockedActions(t *testing.T) {
	snapshot := viewSnapshot()
	snapshot.CapacityOK = false
	snapshot.CapacityNote = "reserva de memória insuficiente"
	snapshot.SelectionNote = "O modelo salvo não está mais disponível; o modelo padrão foi selecionado."
	view := BuildView(ViewState{Loaded: true, Snapshot: snapshot, ActionErr: errors.New("inference_busy")})
	if len(view.Notices) != 3 {
		t.Fatalf("notices = %+v", view.Notices)
	}
	if view.Notices[0].Tone != ToneDanger || !strings.Contains(view.Notices[0].Text, "inferência ativa") {
		t.Fatalf("action error notice = %+v", view.Notices[0])
	}
	if view.Notices[1].Tone != ToneWarn || !strings.Contains(view.Notices[1].Text, "não cabe") {
		t.Fatalf("capacity notice = %+v", view.Notices[1])
	}
	if view.Policy.Load {
		t.Fatal("load offered although the model does not fit")
	}
}

func TestBuildViewClaudeNote(t *testing.T) {
	snapshot := viewSnapshot()
	if note := BuildView(ViewState{Loaded: true, Snapshot: snapshot}).ClaudeNote; note != "Claude Desktop não encontrado" {
		t.Fatalf("note = %q", note)
	}
	snapshot.ClaudeAvailable = true
	if note := BuildView(ViewState{Loaded: true, Snapshot: snapshot}).ClaudeNote; !strings.Contains(note, "oficial disponível") || !strings.Contains(note, "gateway local indisponível") {
		t.Fatalf("note = %q", note)
	}
	snapshot.ClaudeGatewayOK = true
	if note := BuildView(ViewState{Loaded: true, Snapshot: snapshot}).ClaudeNote; !strings.Contains(note, "Anthropic") || !strings.Contains(note, "servidor") {
		t.Fatalf("note = %q", note)
	}
}

func TestSplitDisplayName(t *testing.T) {
	for _, test := range []struct{ in, id, name, detail string }{
		{"Qwen 3.6 35B-A3B | UD-Q2_K_XL | 256k context", "x", "Qwen 3.6 35B-A3B", "UD-Q2_K_XL · 256k context"},
		{"Plain name", "x", "Plain name", ""},
		{"  ", "fallback-id", "fallback-id", ""},
	} {
		name, detail := SplitDisplayName(test.in, test.id)
		if name != test.name || detail != test.detail {
			t.Errorf("SplitDisplayName(%q) = %q, %q", test.in, name, detail)
		}
	}
}

func TestTooltipFitsTheNotificationArea(t *testing.T) {
	snapshot := viewSnapshot()
	snapshot.ActiveModel = "long"
	snapshot.Models = append(snapshot.Models, Model{ID: "long", DisplayName: strings.Repeat("Modelo muito longo ", 12), Available: true})
	if tooltip := BuildView(ViewState{Loaded: true, Snapshot: snapshot}).Tooltip; len([]rune(tooltip)) > 120 {
		t.Fatalf("tooltip has %d runes", len([]rune(tooltip)))
	}
}

func TestPaletteBlendAndColorref(t *testing.T) {
	if got := Blend(0xffffffff, 0x00000000); got != 0xffffffff {
		t.Fatalf("transparent blend = %#x", got)
	}
	if got := Blend(0xffffffff, 0xff000000); got != 0xff000000 {
		t.Fatalf("opaque blend = %#x", got)
	}
	if got := Color(0xff102030).COLORREF(); got != 0x00302010 {
		t.Fatalf("COLORREF = %#x", got)
	}
	for _, palette := range []Palette{LightPalette, DarkPalette} {
		for _, tone := range []Tone{ToneAccent, ToneInfo, ToneWarn, ToneDanger, ToneMuted} {
			if colors := palette.Tone(tone); colors.Ink>>24 != 0xff || colors.Solid>>24 != 0xff {
				t.Errorf("tone %s ink or solid is translucent: ClearType text needs opaque colours", tone)
			}
		}
	}
}
