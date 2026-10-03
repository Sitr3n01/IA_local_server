package trayui

import (
	"fmt"
	"strings"
)

// Tone names the same five states the browser monitor colours its badges
// with, so a state reads the same in the flyout and on the page.
type Tone string

const (
	ToneAccent Tone = "accent"
	ToneInfo   Tone = "info"
	ToneWarn   Tone = "warn"
	ToneDanger Tone = "danger"
	ToneMuted  Tone = "muted"
)

// ModelAction is the one lifecycle step the models section offers for the
// selected model.
type ModelAction int

const (
	ModelActionNone ModelAction = iota
	ModelActionLoad
	ModelActionSwitch
)

// ViewState is everything the flyout shows that is not in the snapshot: the
// action running now and the outcome of the last one.
type ViewState struct {
	Snapshot Snapshot
	// Loaded is false until the first snapshot arrives; until then nothing
	// is known about the server, not even that it is offline.
	Loaded bool
	// Activity is the running lifecycle action, e.g. "Carregando o modelo".
	Activity string
	// Starting is set from the moment the server was asked to start until the
	// edge answers.
	Starting bool
	// ActionErr is the failure of the last action, shown until the next one.
	ActionErr error
}

type Chip struct {
	Text string
	Tone Tone
}

type Notice struct {
	Text string
	Tone Tone
}

type ModelRow struct {
	ID       string
	Name     string
	Detail   string
	Selected bool
	Loaded   bool
}

// View is the flyout's content, decided without Win32 so it can be tested.
type View struct {
	Environment string
	// Tone colours the status pill; IconTone colours the notification-area
	// icon, where an idle but healthy server still shows the brand green.
	Tone        Tone
	IconTone    Tone
	Pill        string
	Title       string
	Subtitle    string
	Progress    bool
	Chips       []Chip
	Notices     []Notice
	Models      []ModelRow
	ModelAction ModelAction
	ShowUnload  bool
	// InFlight counts the requests a shutdown would interrupt.
	InFlight   int
	ClaudeNote string
	Policy     ActionPolicy
	Tooltip    string
}

// BuildView turns a snapshot and the tray's own state into the flyout's
// content.
func BuildView(state ViewState) View {
	snapshot := state.Snapshot
	busy := state.Activity != ""
	view := View{
		Environment: strings.ToUpper(strings.TrimSpace(snapshot.Environment)),
		Policy:      EvaluateActions(snapshot, busy),
	}
	edgeReachable := snapshot.EdgeReachable
	loaded := modelLabel(snapshot.Models, snapshot.ActiveModel)

	switch {
	case !state.Loaded && !busy:
		view.Policy.StartServer = false
		view.Tone, view.IconTone, view.Pill = ToneMuted, ToneMuted, "Conectando"
		view.Title = "Conectando ao servidor"
		view.Subtitle = "Lendo o estado do servidor local."
	case busy:
		view.Tone, view.IconTone, view.Pill = ToneInfo, ToneInfo, "Ocupado"
		view.Title, view.Progress = state.Activity, true
		view.Subtitle = modelLabel(snapshot.Models, snapshot.SelectedModel)
	case !edgeReachable && state.Starting:
		// The start was already asked for; offering it again would only
		// queue a second request behind the first.
		view.Policy.StartServer = false
		view.Tone, view.IconTone, view.Pill = ToneInfo, ToneInfo, "Iniciando"
		view.Title, view.Progress = "Iniciando o servidor", true
		view.Subtitle = "O roteador e o edge estão subindo."
	case !edgeReachable:
		view.Tone, view.IconTone, view.Pill = ToneDanger, ToneDanger, "Offline"
		view.Title = "Servidor offline"
		view.Subtitle = "O servidor local não está respondendo."
	case !snapshot.UpstreamReady:
		view.Tone, view.IconTone, view.Pill = ToneWarn, ToneWarn, "Degradado"
		view.Title = "Servidor degradado"
		view.Subtitle = "O edge responde, mas o roteador de modelos não."
	case !snapshot.ProviderReady && snapshot.ReadyNote != "":
		// The edge is ready only while its default model fits in memory.
		view.Tone, view.IconTone, view.Pill = ToneWarn, ToneWarn, "Sem espaço"
		view.Title = "O modelo não cabe agora"
		view.Subtitle = "Motivo: " + snapshot.ReadyNote + ". Feche programas que usam muita memória."
	case !snapshot.ProviderReady:
		view.Tone, view.IconTone, view.Pill = ToneWarn, ToneWarn, "Indisponível"
		view.Title = "Servidor não está pronto"
		view.Subtitle = "O edge ainda não aceita pedidos."
	case snapshot.Queued > 0:
		view.Tone, view.IconTone, view.Pill = ToneWarn, ToneWarn, "Na fila"
		view.Title, view.Subtitle = "Na fila", loaded
	case snapshot.Active > 0:
		view.Tone, view.IconTone, view.Pill = ToneAccent, ToneInfo, "Em uso"
		view.Title, view.Subtitle = "Em uso", loaded
	case snapshot.ActiveModel != "":
		view.Tone, view.IconTone, view.Pill = ToneAccent, ToneAccent, "Pronto"
		view.Title, view.Subtitle = "Pronto", loaded
	default:
		view.Tone, view.IconTone, view.Pill = ToneMuted, ToneAccent, "Ocioso"
		view.Title = "Ocioso"
		view.Subtitle = "Nenhum modelo na memória. O primeiro pedido carrega o modelo sob demanda."
	}

	if edgeReachable {
		view.InFlight = snapshot.Active + snapshot.Queued
	}
	if edgeReachable && !busy {
		if snapshot.Active > 0 {
			view.Chips = append(view.Chips, Chip{Text: plural(snapshot.Active, "pedido em andamento", "pedidos em andamento"), Tone: ToneAccent})
		}
		if snapshot.Queued > 0 {
			view.Chips = append(view.Chips, Chip{Text: plural(snapshot.Queued, "na fila", "na fila"), Tone: ToneWarn})
		}
	}

	if state.ActionErr != nil {
		view.Notices = append(view.Notices, Notice{Text: FriendlyError(state.ActionErr), Tone: ToneDanger})
	}
	if edgeReachable && !snapshot.StatusAvailable {
		view.Notices = append(view.Notices, Notice{Text: "Status operacional indisponível: carregar e descarregar ficam bloqueados.", Tone: ToneWarn})
	}
	if snapshot.StatusAvailable && snapshot.ProviderReady && !snapshot.CapacityOK && snapshot.CapacityNote != "" && snapshot.ActiveModel != snapshot.SelectedModel && !busy {
		view.Notices = append(view.Notices, Notice{Text: "O modelo selecionado não cabe agora: " + snapshot.CapacityNote + ".", Tone: ToneWarn})
	}
	if snapshot.SelectionNote != "" {
		view.Notices = append(view.Notices, Notice{Text: snapshot.SelectionNote, Tone: ToneInfo})
	}

	for _, model := range snapshot.Models {
		if !model.Available {
			continue
		}
		name, detail := SplitDisplayName(model.DisplayName, model.ID)
		view.Models = append(view.Models, ModelRow{
			ID: model.ID, Name: name, Detail: detail,
			Selected: model.ID == snapshot.SelectedModel,
			Loaded:   model.ID == snapshot.ActiveModel,
		})
	}
	switch {
	case snapshot.ActiveModel == "":
		view.ModelAction = ModelActionLoad
	case snapshot.ActiveModel != snapshot.SelectedModel:
		view.ModelAction = ModelActionSwitch
	}
	view.ShowUnload = snapshot.ActiveModel != ""

	switch {
	case !snapshot.ClaudeAvailable:
		view.ClaudeNote = "Claude Desktop não encontrado"
	case !snapshot.ClaudeGatewayOK:
		view.ClaudeNote = "Claude oficial disponível; gateway local indisponível"
	default:
		view.ClaudeNote = "Conta Anthropic ou modelos deste servidor"
	}

	view.Tooltip = "IA Local · " + view.Pill
	if snapshot.ActiveModel != "" && edgeReachable {
		name, _ := SplitDisplayName(displayName(snapshot.Models, snapshot.ActiveModel), snapshot.ActiveModel)
		view.Tooltip += " · " + name
	}
	// NOTIFYICONDATA holds 128 UTF-16 units including the terminator.
	if runes := []rune(view.Tooltip); len(runes) > 120 {
		view.Tooltip = string(runes[:119]) + "…"
	}
	return view
}

// SplitDisplayName separates a manifest display name such as
// "Qwen 3.8 27B | UD-IQ4_XS | 32k context" into the model and the rest, which
// the flyout shows on two lines.
func SplitDisplayName(displayName, id string) (string, string) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return id, ""
	}
	parts := strings.Split(displayName, "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.Join(parts[1:], " · ")
}

func modelLabel(models []Model, id string) string {
	if id == "" {
		return ""
	}
	name, detail := SplitDisplayName(displayName(models, id), id)
	if detail == "" {
		return name
	}
	return name + " · " + detail
}

func displayName(models []Model, id string) string {
	if model, ok := findModel(models, id); ok {
		return model.DisplayName
	}
	return id
}

func plural(count int, singular, many string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, many)
}

// FriendlyError turns controller errors into one sentence for the operator.
func FriendlyError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "inference_busy"):
		return "Há uma inferência ativa ou aguardando. Tente de novo quando a fila esvaziar."
	case strings.Contains(text, "insufficient_capacity"):
		return "O modelo não cabe com as margens de segurança atuais."
	case strings.Contains(text, "invalid_api_key"), strings.Contains(text, "credential"):
		return "A credencial administrativa está ausente ou inválida."
	case strings.Contains(text, "model_not_found"):
		return "O modelo não está autorizado neste ambiente."
	case strings.Contains(text, "connection refused"), strings.Contains(text, "actively refused"):
		return "O servidor local não está disponível."
	default:
		if runes := []rune(text); len(runes) > 240 {
			text = string(runes[:239]) + "…"
		}
		return text
	}
}
