//go:build windows

package main

import (
	"os"

	// Imported for its init() side effect. The pinned go-webview2 binding
	// neutralizes every WEBVIEW2_* environment override there - see its
	// webviewloader/env_create.go, function preventEnvAndRegistryOverrides -
	// before this or any other package in this process gets a chance to
	// create a WebView2 environment. That is ADR 0018 control 2; this file
	// and env_windows_test.go exist to keep it an executable property rather
	// than an assumption about upstream behaviour.
	"github.com/wailsapp/go-webview2/webviewloader"
)

// hostileWebView2EnvVars are the names ADR 0018 calls out by name as the
// ambient-environment reopening vector control 2 defeats: a serving-user
// process can set these once and turn every subsequent WebView2 launch into
// a Chromium instance with a loopback debugging port, which operates below
// CSP. webviewloader's init() sets each to "" (or "0" for the channel
// preference) rather than unsetting them, because the native loader tests
// for a variable's existence, not its value - which is also what closes the
// equivalent registry-override vector.
var hostileWebView2EnvVars = []string{
	"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
	"WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER",
	"WEBVIEW2_RELEASE_CHANNEL_PREFERENCE",
	"WEBVIEW2_BROWSER_EXECUTABLE_FOLDER",
	"WEBVIEW2_USER_DATA_FOLDER",
}

// sanitizedWebView2EnvSnapshot captures what webviewloader's init() left
// these variables set to. Go's package initialization order guarantees an
// imported package's init() functions (and its package-level variable
// initializers) complete in full before the importing package's own
// package-level variables are initialized - so this map is built strictly
// after webviewloader's init() ran, and strictly before anything in this
// package (including a test's own os.Setenv calls, which only run inside a
// test function body, long after all package-level initialization for the
// whole binary has completed) has any chance to change these variables
// first. env_windows_test.go asserts against this snapshot rather than
// against a live os.Getenv call for exactly that reason: it stays correct
// even after a test deliberately reintroduces hostile values afterward, to
// document the attack this control defeats.
var sanitizedWebView2EnvSnapshot = captureWebView2EnvSnapshot()

func captureWebView2EnvSnapshot() map[string]string {
	snapshot := make(map[string]string, len(hostileWebView2EnvVars))
	for _, name := range hostileWebView2EnvVars {
		snapshot[name] = os.Getenv(name)
	}
	return snapshot
}

// verifyControl2 fails closed at startup if either half of ADR 0018 control
// 2 ever stops holding: the environment left behind by webviewloader's
// init() must match what it has always neutralized these variables to, and
// the pure-Go loader (UsingGoWebview2Loader) must be the one actually in
// use - the property meaning no embedded native DLL is linked in and no
// in-memory PE load ever happens, which only holds when this binary is built
// without the native_webview2loader tag.
func verifyControl2() error {
	want := map[string]string{
		"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS": "",
		"WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER":     "",
		"WEBVIEW2_RELEASE_CHANNEL_PREFERENCE":   "0",
		"WEBVIEW2_BROWSER_EXECUTABLE_FOLDER":    "",
		"WEBVIEW2_USER_DATA_FOLDER":             "",
	}
	for name, expected := range want {
		if got := sanitizedWebView2EnvSnapshot[name]; got != expected {
			return &control2Error{name: name, got: got, want: expected}
		}
	}
	if !webviewloader.UsingGoWebview2Loader {
		return errNativeLoaderActive
	}
	return nil
}

var errNativeLoaderActive = &control2Error{
	name: "UsingGoWebview2Loader",
	got:  "false",
	want: "true (the native_webview2loader build tag must never be set)",
}

type control2Error struct {
	name string
	got  string
	want string
}

func (e *control2Error) Error() string {
	return "ADR 0018 control 2 regression: " + e.name + " = " + e.got + ", want " + e.want
}
