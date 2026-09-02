package main

import (
	"net/url"
	"strings"
	"sync"
)

// navigationPolicy decides which navigations this console may perform, and
// keeps the accounting of what it refused.
//
// It lives in a portable file with no build tag, alongside bridge.go and for
// the same reason: the decision is the security-relevant part, and it should
// be exercised by `go test -race ./...` on the ubuntu-latest CI job rather
// than only on a Windows desktop with a WebView2 runtime attached. The COM
// plumbing that delivers navigations to it is in navigation_windows.go.
//
// The policy is an allowlist of exactly one origin. That is not a
// simplification of something richer - the console is a local operator
// surface served from a virtual host mapping, it has no external
// dependencies, and there is no second place it is ever meant to go.
type navigationPolicy struct {
	// allowedHost is compared whole and case-insensitively. Never by prefix
	// or suffix: `strings.HasSuffix(host, "cia-console.invalid")` would admit
	// `evil-cia-console.invalid`, and a prefix test would admit
	// `cia-console.invalid.attacker.example`. Whole-string equality is the
	// only comparison that cannot be widened by a crafted hostname.
	allowedHost string

	mu       sync.Mutex
	refusals int
	lastURI  string

	// Counted separately from `refusals`: a refused navigation and a refused
	// new window are different holes, and one total would hide either behind
	// the other.
	windowRefusals int
	lastWindowURI  string
}

func newNavigationPolicy(allowedHost string) *navigationPolicy {
	return &navigationPolicy{allowedHost: allowedHost}
}

// permits reports whether one navigation target is allowed. It fails closed:
// anything it cannot parse, anything that is not https, and anything whose
// host is not exactly the allowed host is refused.
//
// The scheme check is not redundant with the host check. A `javascript:` or
// `file:///C:/...` URI has no host at all, so a host-only test would compare
// the empty string against the allowed host and refuse it - correctly, but by
// accident rather than by intent. Requiring https makes the refusal
// deliberate and keeps it correct if allowedHost were ever empty.
func (p *navigationPolicy) permits(uri string) bool {
	parsed, err := url.Parse(uri)
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	if p.allowedHost == "" {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), p.allowedHost)
}

// recordRefusal notes one cancelled navigation. The counter exists so the
// end-to-end harness can prove the guard is *live*, not merely registered: a
// handler that never fires and a handler that fires and permits everything
// look identical from outside the process.
func (p *navigationPolicy) recordRefusal(uri string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refusals++
	p.lastURI = uri
	return p.refusals
}

// recordWindowRefusal notes one refused attempt to open a new window.
//
// Kept separate from recordRefusal because the two close different holes and
// conflating their counts would hide one behind the other. NavigationStarting
// governs where THIS window may go; NewWindowRequested governs whether a
// second window may exist at all. A page that cannot navigate away can still
// call window.open, and a Content-Security-Policy does not stop it: CSP
// governs what a document may fetch, embed and execute, not whether the
// browser may open a window - `connect-src 'none'` closes fetch, XHR and
// WebSocket, and closes nothing about window.open, whose target URL is
// carried in the request line where an attacker can put whatever they want to
// exfiltrate.
func (p *navigationPolicy) recordWindowRefusal(uri string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.windowRefusals++
	p.lastWindowURI = uri
	return p.windowRefusals
}

// WindowStats reports refused new-window attempts.
func (p *navigationPolicy) WindowStats() (refusals int, lastURI string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.windowRefusals, p.lastWindowURI
}

// Stats reports what the policy has refused so far.
func (p *navigationPolicy) Stats() (refusals int, lastURI string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refusals, p.lastURI
}
