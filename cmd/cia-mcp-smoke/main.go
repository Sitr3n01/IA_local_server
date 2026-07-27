package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sitr3n/local-ai-provider/internal/mcpinference"
)

const toolName = "local_ai_delegate"

type report struct {
	Status          string             `json:"status"`
	Server          string             `json:"server"`
	Tool            string             `json:"tool"`
	Model           string             `json:"model,omitempty"`
	FinishReason    string             `json:"finish_reason,omitempty"`
	OutputBytes     int                `json:"output_bytes,omitempty"`
	OutputSHA256    string             `json:"output_sha256,omitempty"`
	ExpectedMatched bool               `json:"expected_text_matched"`
	Usage           mcpinference.Usage `json:"usage,omitempty"`
}

func main() {
	server := flag.String("server", `C:\IA\local-ai-v2\bin\cia-mcp-inference.exe`, "installed MCP inference server")
	dataURL := flag.String("data-url", "http://127.0.0.1:18090", "literal-loopback edge origin")
	model := flag.String("model", "local-coding", "pinned local model")
	expected := flag.String("expected", "CIA_LOCAL_MCP_SMOKE_OK_42", "exact synthetic response expected from the local model")
	timeout := flag.Duration("timeout", 6*time.Minute, "whole MCP smoke-test timeout")
	flag.Parse()

	rep, err := run(*server, *dataURL, *model, *expected, *timeout)
	if encodeErr := json.NewEncoder(os.Stdout).Encode(rep); encodeErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "cia-mcp-smoke: could not encode the metadata-only report")
		os.Exit(1)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cia-mcp-smoke: %v\n", err)
		os.Exit(1)
	}
}

func run(server, dataURL, model, expected string, timeout time.Duration) (report, error) {
	rep := report{Status: "failed", Server: server, Tool: toolName}
	if strings.TrimSpace(server) == "" || strings.TrimSpace(expected) == "" {
		return rep, errors.New("server and expected marker are required")
	}
	if timeout <= 0 || timeout > 15*time.Minute {
		return rep, errors.New("timeout must be greater than zero and at most 15 minutes")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	command := exec.CommandContext(ctx, server)
	command.Env = append(os.Environ(),
		"CIA_MCP_INFERENCE_DATA_URL="+dataURL,
		"CIA_MCP_INFERENCE_MODEL="+model,
		"CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS=128",
	)
	client := mcp.NewClient(&mcp.Implementation{Name: "cia-mcp-smoke", Version: "dev"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return rep, errors.New("MCP handshake failed")
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return rep, errors.New("MCP tool discovery failed")
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != toolName {
		return rep, errors.New("MCP server exposed an unexpected tool surface")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: toolName,
		Arguments: map[string]any{
			"prompt":            "Return exactly " + expected + " and nothing else.",
			"max_output_tokens": 128,
		},
	})
	if err != nil {
		return rep, errors.New("MCP tool call failed")
	}
	if result.IsError {
		return rep, errors.New("local delegation returned a sanitized tool error")
	}

	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return rep, errors.New("MCP structured output could not be decoded")
	}
	var output mcpinference.DelegateOutput
	if err := json.Unmarshal(payload, &output); err != nil {
		return rep, errors.New("MCP structured output did not match the contract")
	}
	digest := sha256.Sum256([]byte(output.Text))
	rep.Model = output.Model
	rep.FinishReason = output.FinishReason
	rep.OutputBytes = len(output.Text)
	rep.OutputSHA256 = strings.ToUpper(hex.EncodeToString(digest[:]))
	rep.ExpectedMatched = strings.TrimSpace(output.Text) == expected
	rep.Usage = output.Usage
	if !rep.ExpectedMatched {
		rep.Status = "mismatch"
		return rep, errors.New("local model did not return the exact synthetic marker")
	}
	rep.Status = "ok"
	return rep, nil
}
