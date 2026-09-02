//go:build windows

package main

import (
	"testing"

	"github.com/wailsapp/go-webview2/webviewloader"
)

// TestHostileWebView2EnvironmentIsNeutralized is the executable proof ADR
// 0018 control 2 requires: the pinned go-webview2 binding neutralizes every
// WEBVIEW2_* override in its own package init(), before this or any other
// package can create a WebView2 environment.
//
// Honesty note on what this test can and cannot prove: Go calls an imported
// package's init() exactly once per binary, strictly before the importing
// package's own package-level variable initializers run (see the comment on
// sanitizedWebView2EnvSnapshot in env_windows.go). That ordering guarantee is
// what makes this test deterministic and installation-independent: it never
// needs the WebView2 runtime installed, and it never asks the loader to
// actually create an environment - doing so would start a real Edge/WebView2
// browser process in the background (see webviewloader/env_create.go,
// createWebViewEnvironmentWithClientDll), which this spike must not do from
// a unit test on a memory-constrained machine. But it also means the
// t.Setenv calls below, which restage the ADR's own hostile recipe purely to
// document the attack, run AFTER init() already completed. Nothing in this
// package re-scrubs the environment on every read; only the loader's own
// environment-creation path calls preventEnvAndRegistryOverrides() a second
// time (immediately before its native call), and this test deliberately
// never reaches that path. So this test proves the property a fresh process
// actually depends on - what init() leaves behind before first use - and
// does NOT prove that setting these variables later, from inside an
// already-running console process, would be re-neutralized before the next
// environment creation. The ADR's own manual verification proved that
// stronger claim by setting the hostile values in a PARENT shell and then
// launching a separate probe process, so that the probe's own init() was the
// one observed reacting to them. A stronger, automated version of this test
// would reproduce that with a real process boundary: exec a child test
// binary (or a small dedicated probe) with the hostile values placed
// directly in its inherited environment, and assert on what the child
// reports over its stdout, rather than on a same-process snapshot.
func TestHostileWebView2EnvironmentIsNeutralized(t *testing.T) {
	want := map[string]string{
		"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS": "",
		"WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER":     "",
		"WEBVIEW2_RELEASE_CHANNEL_PREFERENCE":   "0",
		"WEBVIEW2_BROWSER_EXECUTABLE_FOLDER":    "",
		"WEBVIEW2_USER_DATA_FOLDER":             "",
	}
	for name, expected := range want {
		got, ok := sanitizedWebView2EnvSnapshot[name]
		if !ok {
			t.Fatalf("snapshot is missing %s", name)
		}
		if got != expected {
			t.Errorf("webviewloader left %s = %q after init, want %q (control 2 regression)", name, got, expected)
		}
	}

	// The property meaning no embedded native DLL is linked in and no
	// in-memory PE load ever happens: v1.0.23 only takes that path under the
	// native_webview2loader build tag, which this project's build must never
	// set (see docs/adr/0018-webview-operator-console.md).
	if !webviewloader.UsingGoWebview2Loader {
		t.Fatal("webviewloader.UsingGoWebview2Loader is false: the native, embedded-DLL loader is active")
	}

	if err := verifyControl2(); err != nil {
		t.Fatalf("verifyControl2: %v", err)
	}

	// Restage the ADR's own hostile recipe (docs/adr/0018-webview-operator-
	// console.md, control 2 verification) purely so this test file documents
	// the exact attack the control defeats. t.Setenv restores the prior
	// value automatically at the end of the test. Per the honesty note
	// above, these values are NOT reasserted as neutralized afterward -
	// doing so would be false, since nothing in this process re-scrubs the
	// environment just because time passes.
	hostile := map[string]string{
		"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS": "--remote-debugging-port=9222",
		"WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER":     "1",
		"WEBVIEW2_RELEASE_CHANNEL_PREFERENCE":   "1",
	}
	for name, value := range hostile {
		t.Setenv(name, value)
	}
}
