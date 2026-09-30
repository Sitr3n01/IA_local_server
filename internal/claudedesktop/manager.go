// Package claudedesktop owns the narrowly-scoped, reversible Claude Desktop
// third-party-inference profile used by CIA. It never reads or writes the
// first-party Claude profile.
package claudedesktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	profileID                 = "077f3f5a-e971-4d10-8e11-3641069cf0e1"
	profileName               = "CIA Local Gateway"
	backupSchemaVersion       = 1
	maxProfileBytes     int64 = 1 << 20
)

// Mode represents the deployment selected on the next full Claude Desktop
// launch. Anthropic only changes the isolated 3P selector to force the
// untouched first-party profile back into use.
type Mode string

const (
	ModeAnthropic Mode = "anthropic"
	ModeLocal     Mode = "local"
)

// Identity is resolved from the installed MSIX package. Package paths and
// versions are intentionally absent from the persisted CIA state.
type Identity struct {
	PackageFamilyName string `json:"package_family_name"`
	AppUserModelID    string `json:"app_user_model_id"`
	Version           string `json:"version"`
	InstallLocation   string `json:"install_location"`
}

func (i Identity) Validate() error {
	if strings.TrimSpace(i.PackageFamilyName) == "" || strings.TrimSpace(i.AppUserModelID) == "" || strings.TrimSpace(i.Version) == "" || strings.TrimSpace(i.InstallLocation) == "" {
		return errors.New("claude Desktop MSIX identity is incomplete")
	}
	if strings.ContainsAny(i.AppUserModelID+i.InstallLocation, "\r\n") || !filepath.IsAbs(i.InstallLocation) {
		return errors.New("claude Desktop App User Model ID is invalid")
	}
	return nil
}

// Gateway contains the one endpoint that Local mode may use. BaseURL is
// validated as a literal loopback HTTP origin, never a DNS name, LAN address,
// HTTPS proxy, or path with a hidden upstream.
type Gateway struct {
	BaseURL string
	APIKey  string
}

// ModelProbe is the credential-safe result of one real Anthropic Messages
// generation. It deliberately excludes request headers and response text: the
// command-line probe needs proof that a model produced a Claude-compatible
// content block, not a new place where credentials or prompt content can leak.
type ModelProbe struct {
	Model        string   `json:"model"`
	StopReason   string   `json:"stop_reason"`
	ContentKinds []string `json:"content_kinds"`
}

func (g Gateway) Validate() error {
	if len(g.APIKey) < 32 || len(g.APIKey) > 4096 || strings.ContainsAny(g.APIKey, "\r\n") {
		return errors.New("claude gateway credential must contain 32 to 4096 characters without line breaks")
	}
	if strings.TrimSpace(g.BaseURL) != g.BaseURL {
		return errors.New("claude gateway URL must not contain surrounding whitespace")
	}
	parsed, err := url.Parse(g.BaseURL)
	if err != nil {
		return fmt.Errorf("parse claude gateway URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("claude gateway URL must be a plain loopback HTTP origin")
	}
	host := net.ParseIP(strings.Trim(parsed.Hostname(), "[]"))
	if host == nil || !host.IsLoopback() || parsed.Port() == "" {
		return errors.New("claude gateway URL must use a literal loopback address and explicit port")
	}
	return nil
}

// ProbeGateway proves that the dedicated Claude credential can discover a
// local model without issuing a prompt or loading one. It is safe to run from
// the tray refresh loop and before a Local-mode transition.
func ProbeGateway(ctx context.Context, gateway Gateway) error {
	if err := gateway.Validate(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(gateway.BaseURL, "/")+"/v1/models?limit=1000", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+gateway.APIKey)
	request.Header.Set("anthropic-version", "2023-06-01")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("CIA Claude gateway discovery: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("CIA Claude gateway discovery returned %s", response.Status)
	}
	var payload struct {
		Data []struct {
			ID                  string `json:"id"`
			AnthropicFamilyTier string `json:"anthropic_family_tier"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProfileBytes+1)).Decode(&payload); err != nil {
		return fmt.Errorf("decode CIA Claude gateway discovery: %w", err)
	}
	eligible := 0
	for _, model := range payload.Data {
		id := strings.TrimSpace(model.ID)
		tier := strings.ToLower(strings.TrimSpace(model.AnthropicFamilyTier))
		if id != "" && (strings.HasPrefix(strings.ToLower(id), "claude-") || tier == "haiku" || tier == "sonnet" || tier == "opus") {
			eligible++
		}
	}
	if eligible == 0 {
		return errors.New("CIA Claude gateway returned no eligible models")
	}
	return nil
}

// ProbeModel performs one real, non-streaming Anthropic Messages request for a
// specific model. Unlike ProbeGateway this can load weights and run inference,
// so it is exposed only through an explicit diagnostic command and never from
// the tray refresh loop.
func ProbeModel(ctx context.Context, gateway Gateway, model string) (ModelProbe, error) {
	if err := gateway.Validate(); err != nil {
		return ModelProbe{}, err
	}
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 256 || strings.ContainsAny(model, "\r\n\t") {
		return ModelProbe{}, errors.New("claude model probe requires a valid model ID")
	}
	payload := map[string]any{
		"model": model,
		// Reasoning models may consume a few hundred hidden tokens before
		// emitting their first text block. Keep the probe bounded, but give it
		// enough room to prove that a final answer is actually produced.
		"max_tokens": 2048,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "Reply with exactly CIA_CLAUDE_MODEL_OK in a text block. Do not call a tool.",
		}},
		// Claude Cowork supplies tools even for requests that should answer in
		// text. Including one harmless definition proves both the function-call
		// path and the intentional toolless-model fallback.
		"tools": []map[string]any{{
			"name":         "local_probe",
			"description":  "Diagnostic tool that must not be called.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ModelProbe{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(gateway.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return ModelProbe{}, err
	}
	request.Header.Set("Authorization", "Bearer "+gateway.APIKey)
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 10 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return ModelProbe{}, fmt.Errorf("CIA Claude model probe: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ModelProbe{}, fmt.Errorf("CIA Claude model probe for %s returned %s", model, response.Status)
	}
	var decoded struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		} `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProfileBytes+1)).Decode(&decoded); err != nil {
		return ModelProbe{}, fmt.Errorf("decode CIA Claude model probe: %w", err)
	}
	if decoded.Type != "message" || len(decoded.Content) == 0 {
		return ModelProbe{}, errors.New("CIA Claude model probe returned no content blocks")
	}
	result := ModelProbe{Model: model, StopReason: decoded.StopReason, ContentKinds: make([]string, 0, len(decoded.Content))}
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) == "" {
				return ModelProbe{}, errors.New("CIA Claude model probe returned an empty text block")
			}
		case "tool_use":
			if strings.TrimSpace(block.Name) == "" {
				return ModelProbe{}, errors.New("CIA Claude model probe returned an invalid tool block")
			}
		default:
			return ModelProbe{}, fmt.Errorf("CIA Claude model probe returned unsupported content type %q", block.Type)
		}
		result.ContentKinds = append(result.ContentKinds, block.Type)
	}
	return result, nil
}

// Paths identifies only the isolated 3P state. FirstPartyConfigPath is an
// evidence-only field used by preflight to prove the manager is not targeting
// it; no manager operation opens it for writing.
type Paths struct {
	ThirdPartyStateDir string
	FirstPartyConfig   string
	BackupDir          string
}

func (p Paths) Validate() error {
	for label, path := range map[string]string{
		"Claude Desktop 3P state directory": p.ThirdPartyStateDir,
		"Claude Desktop 1P configuration":   p.FirstPartyConfig,
		"CIA Claude backup directory":       p.BackupDir,
	} {
		if strings.TrimSpace(path) != path || !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", label)
		}
	}
	return nil
}

func (p Paths) configLibrary() string { return filepath.Join(p.ThirdPartyStateDir, "configLibrary") }
func (p Paths) profilePath() string   { return filepath.Join(p.configLibrary(), profileID+".json") }
func (p Paths) metaPath() string      { return filepath.Join(p.configLibrary(), "_meta.json") }
func (p Paths) modePath() string {
	return filepath.Join(p.ThirdPartyStateDir, "claude_desktop_config.json")
}
func (p Paths) backupPath() string { return filepath.Join(p.BackupDir, "claude-desktop-3p.backup") }

// PolicyReader detects enterprise policies that take precedence over the
// local config library. The manager stops before touching state in that case.
type PolicyReader interface {
	ManagedInference(context.Context) (bool, error)
}

// Restarter owns process shutdown/relaunch. Its implementation never passes a
// gateway credential on a command line.
type Restarter interface {
	Restart(context.Context, Identity) error
}

// Verifier observes the post-restart result. It must not infer successful
// routing merely because a profile was written.
type Verifier interface {
	Verify(context.Context, Mode, Gateway) error
}

// Protector provides user-scoped protection for the full pre-mutation 3P
// snapshot. Windows callers use DPAPI; tests can use a deterministic fake.
type Protector interface {
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
}

type Manager struct {
	paths     Paths
	identity  Identity
	policy    PolicyReader
	restarter Restarter
	verifier  Verifier
	protector Protector
	mu        sync.Mutex
}

func NewManager(paths Paths, identity Identity, policy PolicyReader, restarter Restarter, verifier Verifier, protector Protector) (*Manager, error) {
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if policy == nil || restarter == nil || verifier == nil || protector == nil {
		return nil, errors.New("claude Desktop manager dependencies are required")
	}
	return &Manager{paths: paths, identity: identity, policy: policy, restarter: restarter, verifier: verifier, protector: protector}, nil
}

// Apply performs the only permitted mutation sequence:
// precheck -> DPAPI backup -> atomic write -> restart -> verify -> commit.
// A failed write, restart, or verification restores every 3P byte captured by
// the backup and asks the same restarter to bring Claude back up.
func (m *Manager) Apply(ctx context.Context, mode Mode, gateway Gateway) (err error) {
	if mode != ModeAnthropic && mode != ModeLocal {
		return fmt.Errorf("unsupported Claude Desktop mode %q", mode)
	}
	if mode == ModeLocal {
		if err := gateway.Validate(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	managed, err := m.policy.ManagedInference(ctx)
	if err != nil {
		return fmt.Errorf("precheck managed Claude policy: %w", err)
	}
	if managed {
		return errors.New("claude Desktop has a managed inference policy; CIA will not override it")
	}
	if m.configuredFor(mode, gateway) {
		// A repeated tray selection should not close and reopen Desktop when both
		// persisted state and the running renderer already match. A failed live
		// verification falls through to the normal transactional restart path.
		if err := m.verifier.Verify(ctx, mode, gateway); err == nil {
			return nil
		}
	}

	backup, err := m.captureBackup()
	if err != nil {
		return fmt.Errorf("backup Claude Desktop 3P state: %w", err)
	}
	if err := m.writeBackup(backup); err != nil {
		return fmt.Errorf("protect Claude Desktop 3P backup: %w", err)
	}

	rollback := func(cause error) error {
		restoreErr := m.restoreBackup(backup)
		restartErr := m.restarter.Restart(context.Background(), m.identity)
		if restoreErr != nil || restartErr != nil {
			return fmt.Errorf("%w; rollback restore=%v restart=%v", cause, restoreErr, restartErr)
		}
		return cause
	}

	if err := m.writeMode(mode, gateway); err != nil {
		return rollback(fmt.Errorf("write Claude Desktop 3P state: %w", err))
	}
	if err := m.restarter.Restart(ctx, m.identity); err != nil {
		return rollback(fmt.Errorf("restart Claude Desktop: %w", err))
	}
	if err := m.verifier.Verify(ctx, mode, gateway); err != nil {
		return rollback(fmt.Errorf("verify Claude Desktop mode: %w", err))
	}
	return nil
}

func (m *Manager) configuredFor(mode Mode, gateway Gateway) bool {
	modeDocument, err := readJSONObject(m.paths.modePath())
	if err != nil {
		return false
	}
	expectedMode := "1p"
	if mode == ModeLocal {
		expectedMode = "3p"
	}
	if current, _ := modeDocument["deploymentMode"].(string); current != expectedMode {
		return false
	}
	if mode == ModeAnthropic {
		return true
	}
	profile, err := readJSONObject(m.paths.profilePath())
	if err != nil {
		return false
	}
	for key, expected := range map[string]string{
		"inferenceProvider":          "gateway",
		"inferenceCredentialKind":    "static",
		"inferenceGatewayBaseUrl":    gateway.BaseURL,
		"inferenceGatewayApiKey":     gateway.APIKey,
		"inferenceGatewayAuthScheme": "bearer",
	} {
		if actual, _ := profile[key].(string); actual != expected {
			return false
		}
	}
	meta, err := readJSONObject(m.paths.metaPath())
	if err != nil {
		return false
	}
	appliedID, _ := meta["appliedId"].(string)
	return appliedID == profileID
}

type backupDocument struct {
	SchemaVersion int         `json:"schema_version"`
	Files         []savedFile `json:"files"`
}

type savedFile struct {
	Name     string `json:"name"`
	Exists   bool   `json:"exists"`
	Contents []byte `json:"contents,omitempty"`
}

func (m *Manager) captureBackup() (backupDocument, error) {
	files := []struct {
		name string
		path string
	}{
		{"profile", m.paths.profilePath()},
		{"meta", m.paths.metaPath()},
		{"mode", m.paths.modePath()},
	}
	backup := backupDocument{SchemaVersion: backupSchemaVersion, Files: make([]savedFile, 0, len(files))}
	for _, file := range files {
		contents, err := readOptionalFile(file.path, maxProfileBytes)
		if err != nil {
			return backupDocument{}, err
		}
		backup.Files = append(backup.Files, savedFile{Name: file.name, Exists: contents != nil, Contents: contents})
	}
	return backup, nil
}

func (m *Manager) writeBackup(backup backupDocument) error {
	plain, err := json.Marshal(backup)
	if err != nil {
		return err
	}
	protected, err := m.protector.Protect(plain)
	if err != nil {
		return err
	}
	return writeAtomic(m.paths.backupPath(), protected)
}

// ReadBackup verifies and decrypts the latest snapshot without exposing it in
// diagnostics. It primarily exists for a deliberate restore command.
func (m *Manager) ReadBackup() error {
	data, err := os.ReadFile(m.paths.backupPath())
	if err != nil {
		return err
	}
	plain, err := m.protector.Unprotect(data)
	if err != nil {
		return err
	}
	var backup backupDocument
	if err := strictJSON(plain, &backup); err != nil {
		return err
	}
	if backup.SchemaVersion != backupSchemaVersion {
		return errors.New("unsupported Claude Desktop backup schema")
	}
	return nil
}

// CurrentMode reads only the isolated selector. It never opens the first-party
// profile and returns Anthropic when no 3P selector has been created yet.
func (m *Manager) CurrentMode() (Mode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	document, err := readJSONObject(m.paths.modePath())
	if err != nil {
		return "", err
	}
	if current, _ := document["deploymentMode"].(string); current == "3p" {
		return ModeLocal, nil
	}
	return ModeAnthropic, nil
}

func (m *Manager) restoreBackup(backup backupDocument) error {
	for _, file := range backup.Files {
		var path string
		switch file.Name {
		case "profile":
			path = m.paths.profilePath()
		case "meta":
			path = m.paths.metaPath()
		case "mode":
			path = m.paths.modePath()
		default:
			return errors.New("backup contains an unknown file")
		}
		if file.Exists {
			if err := writeAtomic(path, file.Contents); err != nil {
				return err
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *Manager) writeMode(mode Mode, gateway Gateway) error {
	modeDocument, err := readJSONObject(m.paths.modePath())
	if err != nil {
		return err
	}
	if mode == ModeLocal {
		profile, err := gatewayProfile(gateway)
		if err != nil {
			return err
		}
		if err := writeJSONAtomic(m.paths.profilePath(), profile); err != nil {
			return err
		}
		meta, err := readJSONObject(m.paths.metaPath())
		if err != nil {
			return err
		}
		if err := selectProfile(meta); err != nil {
			return err
		}
		if err := writeJSONAtomic(m.paths.metaPath(), meta); err != nil {
			return err
		}
		modeDocument["deploymentMode"] = "3p"
	} else {
		// This 1P selector is intentionally stored in the isolated 3P state.
		// The existing %APPDATA%\\Claude config and all 1P data remain untouched.
		modeDocument["deploymentMode"] = "1p"
	}
	return writeJSONAtomic(m.paths.modePath(), modeDocument)
}

func gatewayProfile(g Gateway) (map[string]any, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	// Omitting inferenceModels is intentional: the current Claude Desktop 3P
	// Gateway provider discovers every eligible model from GET /v1/models.
	return map[string]any{
		"inferenceProvider":          "gateway",
		"inferenceCredentialKind":    "static",
		"inferenceGatewayBaseUrl":    g.BaseURL,
		"inferenceGatewayApiKey":     g.APIKey,
		"inferenceGatewayAuthScheme": "bearer",
	}, nil
}

func selectProfile(meta map[string]any) error {
	entries, _ := meta["entries"].([]any)
	found := false
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		if !ok {
			return errors.New("claude Desktop 3P metadata has an invalid entry")
		}
		if id, _ := item["id"].(string); id == profileID {
			item["name"] = profileName
			found = true
		}
	}
	if !found {
		entries = append(entries, map[string]any{"id": profileID, "name": profileName})
	}
	meta["entries"] = entries
	meta["appliedId"] = profileID
	return nil
}

func readJSONObject(path string) (map[string]any, error) {
	data, err := readOptionalFile(path, maxProfileBytes)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return map[string]any{}, nil
	}
	var document map[string]any
	if err := strictJSON(data, &document); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	if document == nil {
		return nil, fmt.Errorf("decode %s: expected JSON object", filepath.Base(path))
	}
	return document, nil
}

func writeJSONAtomic(path string, document map[string]any) error {
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func readOptionalFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), maximum)
	}
	return bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), nil
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func writeAtomic(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cia-claude-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// ProfileFingerprint reports a non-secret identity for a profile document.
// It is useful in status output without exposing the endpoint credential.
func ProfileFingerprint(profile []byte) string {
	sum := sha256.Sum256(profile)
	return hex.EncodeToString(sum[:8])
}
