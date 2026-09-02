package main

import (
	"sync"
	"testing"
)

// The console's navigation allowlist is one origin. These cases are written
// as the attacker would write them: every refusal below is a hostname or
// scheme that a prefix test, a suffix test, or a host-only test would let
// through.
func TestNavigationPolicyPermits(t *testing.T) {
	t.Parallel()

	const host = "cia-console.invalid"
	policy := newNavigationPolicy(host)

	permitted := []string{
		"https://cia-console.invalid/",
		"https://cia-console.invalid/index.html",
		"https://cia-console.invalid/assets/index-abc123.js",
		"https://cia-console.invalid/index.html?x=1#frag",
		// Host comparison is case-insensitive, as DNS is.
		"https://CIA-CONSOLE.INVALID/index.html",
		// A port does not change the host, and the virtual host mapping has
		// no port of its own to compare against.
		"https://cia-console.invalid:443/index.html",
	}
	for _, uri := range permitted {
		if !policy.permits(uri) {
			t.Errorf("permits(%q) = false, want true", uri)
		}
	}

	refused := map[string]string{
		// Suffix matching would admit this.
		"https://evil-cia-console.invalid/": "host merely ends with the allowed host",
		"https://not-cia-console.invalid/":  "host merely ends with the allowed host",
		// Prefix matching would admit these two.
		"https://cia-console.invalid.attacker.test/": "host merely begins with the allowed host",
		"https://cia-console.invalidate.test/":       "host merely begins with the allowed host",
		// Substring matching would admit this.
		"https://a.cia-console.invalid.b.test/": "allowed host appears in the middle",
		// A subdomain is a different host and is not implied by the allowlist.
		"https://sub.cia-console.invalid/": "subdomain of the allowed host",
		// Userinfo is the classic way to make a hostile host look allowed to
		// a human reading the bar; url.Hostname() is not fooled, and this
		// pins that.
		"https://cia-console.invalid@attacker.test/": "allowed host in the userinfo, not the host",
		// Schemes other than https, including ones with no host at all.
		"http://cia-console.invalid/":  "plaintext scheme",
		"file:///C:/Windows/System32/": "local file scheme",
		"javascript:alert(1)":          "script scheme",
		"data:text/html,<h1>x</h1>":    "inline document scheme",
		"about:blank":                  "about scheme",
		"vbscript:msgbox(1)":           "script scheme",
		"ms-appx-web://x/y":            "app package scheme",
		// Empty and malformed input.
		"":            "empty target",
		"://nonsense": "unparseable target",
	}
	for uri, why := range refused {
		if policy.permits(uri) {
			t.Errorf("permits(%q) = true, want false (%s)", uri, why)
		}
	}
}

// An empty allowed host must refuse everything rather than matching every
// hostless URI. Without the explicit guard this would admit any scheme-only
// URI whose Hostname() is also "".
func TestNavigationPolicyEmptyHostRefusesEverything(t *testing.T) {
	t.Parallel()

	policy := newNavigationPolicy("")
	for _, uri := range []string{"https://cia-console.invalid/", "https:///", "about:blank", ""} {
		if policy.permits(uri) {
			t.Errorf("permits(%q) = true with no allowed host configured, want false", uri)
		}
	}
}

// Refusals are counted from WebView2's own event thread while the harness
// reads them from a test goroutine, so the accounting has to be safe under
// -race. This is the assertion the ubuntu-latest race job actually exercises.
func TestNavigationPolicyRefusalAccountingIsRaceFree(t *testing.T) {
	t.Parallel()

	policy := newNavigationPolicy("cia-console.invalid")

	const writers, each = 8, 50
	var wg sync.WaitGroup
	wg.Add(writers * 2)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				policy.recordRefusal("https://attacker.test/")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				_, _ = policy.Stats()
			}
		}()
	}
	wg.Wait()

	refusals, lastURI := policy.Stats()
	if want := writers * each; refusals != want {
		t.Errorf("refusals = %d, want %d", refusals, want)
	}
	if lastURI != "https://attacker.test/" {
		t.Errorf("lastURI = %q, want the refused target", lastURI)
	}
}

// A guard that has never refused anything is not evidence of anything. This
// pins that the zero state is distinguishable from a real refusal, which is
// what lets the end-to-end harness assert the guard is live rather than
// merely registered.
func TestNavigationPolicyStartsWithNoRefusals(t *testing.T) {
	t.Parallel()

	refusals, lastURI := newNavigationPolicy("cia-console.invalid").Stats()
	if refusals != 0 || lastURI != "" {
		t.Errorf("a fresh policy reports %d refusal(s) of %q, want 0 and empty", refusals, lastURI)
	}
}
