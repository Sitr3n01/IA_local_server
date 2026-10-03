package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

const testListen = "127.0.0.1:18095"

func newTestHandler(t *testing.T, sampled bool) http.Handler {
	t.Helper()
	collector, _ := newTestCollector(t, fakeEdge(t, serveInference(inferenceJSON)), &fakeSampler{})
	if sampled {
		collector.refreshStatus(context.Background())
		collector.tick(context.Background())
	}
	handler, err := NewHandler(collector, testListen)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func get(handler http.Handler, method, host, path string, header map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+host+path, nil)
	request.Host = host
	for key, value := range header {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestHandlerAnswersOnlyItsOwnHostNames(t *testing.T) {
	handler := newTestHandler(t, true)
	for _, host := range []string{"127.0.0.1:18095", "localhost:18095", "LOCALHOST:18095"} {
		if code := get(handler, http.MethodGet, host, "/api/snapshot", nil).Code; code != http.StatusOK {
			t.Errorf("Host %s = %d, want 200", host, code)
		}
	}
	// A rebinding page sends its own name; another port is another service.
	for _, host := range []string{"attacker.example:18095", "127.0.0.1:18096", "127.0.0.1", "[::1]:18095", "localhost.attacker.example:18095"} {
		if code := get(handler, http.MethodGet, host, "/api/snapshot", nil).Code; code != http.StatusForbidden {
			t.Errorf("Host %s = %d, want 403", host, code)
		}
	}
}

func TestHandlerIsReadOnly(t *testing.T) {
	handler := newTestHandler(t, true)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		recorder := get(handler, method, testListen, "/api/snapshot", nil)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s = %d Allow=%q, want 405 GET, HEAD", method, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
	if recorder := get(handler, http.MethodHead, testListen, "/", nil); recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("HEAD / = %d with %d body bytes", recorder.Code, recorder.Body.Len())
	}
}

func TestHandlerSendsTheSecurityHeadersOnEveryResponse(t *testing.T) {
	handler := newTestHandler(t, true)
	for _, probe := range []struct{ method, host, path string }{
		{http.MethodGet, testListen, "/"},
		{http.MethodGet, testListen, "/api/snapshot"},
		{http.MethodGet, testListen, "/assets/app.js"},
		{http.MethodGet, testListen, "/missing"},
		{http.MethodPost, testListen, "/"},
		{http.MethodGet, "attacker.example:18095", "/"},
	} {
		header := get(handler, probe.method, probe.host, probe.path, nil).Header()
		for name, want := range map[string]string{
			"Content-Security-Policy": contentSecurityPolicy,
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
			"Referrer-Policy":         "no-referrer",
		} {
			if got := header.Get(name); got != want {
				t.Errorf("%s %s %s: %s = %q, want %q", probe.method, probe.host, probe.path, name, got, want)
			}
		}
		if header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s %s: the monitor must not grant cross-origin reads", probe.method, probe.path)
		}
	}
	if !strings.Contains(contentSecurityPolicy, "frame-ancestors 'none'") || strings.Contains(contentSecurityPolicy, "unsafe") {
		t.Fatalf("policy %q must forbid framing and never allow unsafe sources", contentSecurityPolicy)
	}
}

func TestHandlerServesTheSnapshotUncached(t *testing.T) {
	cold := get(newTestHandler(t, false), http.MethodGet, testListen, "/api/snapshot", nil)
	if cold.Code != http.StatusServiceUnavailable || cold.Header().Get("Retry-After") != "1" {
		t.Fatalf("before the first sample = %d Retry-After=%q, want 503 1", cold.Code, cold.Header().Get("Retry-After"))
	}
	warm := get(newTestHandler(t, true), http.MethodGet, testListen, "/api/snapshot", nil)
	if warm.Code != http.StatusOK || warm.Header().Get("Cache-Control") != "no-store" ||
		warm.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("snapshot = %d %v", warm.Code, warm.Header())
	}
}

func TestHandlerServesAssetsWithFixedTypesAndRevalidation(t *testing.T) {
	handler := newTestHandler(t, true)
	for path, contentType := range map[string]string{
		"/":                      "text/html; charset=utf-8",
		"/assets/app.js":         "text/javascript; charset=utf-8",
		"/assets/theme.js":       "text/javascript; charset=utf-8",
		"/assets/tokens.css":     "text/css; charset=utf-8",
		"/assets/components.css": "text/css; charset=utf-8",
		"/assets/app.css":        "text/css; charset=utf-8",
		"/assets/icon.svg":       "image/svg+xml",
	} {
		first := get(handler, http.MethodGet, testListen, path, nil)
		if first.Code != http.StatusOK || first.Header().Get("Content-Type") != contentType || first.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s = %d %q %q", path, first.Code, first.Header().Get("Content-Type"), first.Header().Get("Cache-Control"))
			continue
		}
		etag := first.Header().Get("ETag")
		if second := get(handler, http.MethodGet, testListen, path, map[string]string{"If-None-Match": etag}); second.Code != http.StatusNotModified {
			t.Errorf("%s revalidation = %d, want 304", path, second.Code)
		}
	}
	for _, path := range []string{"/index.html", "/assets/", "/assets/../web/index.html", "/web/index.html", "/assets/app.js.map", "/favicon.ico"} {
		if code := get(handler, http.MethodGet, testListen, path, nil).Code; code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, code)
		}
	}
}

// The policy forbids inline script and style, so the page must not depend on
// either: an inline handler or style attribute would silently do nothing.
func TestThePageNeedsNothingThePolicyForbids(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for pattern, why := range map[string]string{
		`<script(\s[^>]*)?>\s*[^<\s]`: "an inline script",
		`\sstyle\s*=`:                 "an inline style attribute",
		`<style`:                      "a style element",
		`\son[a-z]+\s*=`:              "an inline event handler",
		`(?i)https?://`:               "a reference to another origin",
	} {
		if match := regexp.MustCompile(pattern).FindString(page); match != "" {
			t.Errorf("index.html has %s: %q", why, match)
		}
	}
	for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(page, -1) {
		if !strings.Contains(tag, ` src="/assets/`) {
			t.Errorf("index.html has a script that is not one of the page's own files: %s", tag)
		}
	}

	handler := newTestHandler(t, true)
	for _, reference := range regexp.MustCompile(`(?:src|href)="(/[^"]*)"`).FindAllStringSubmatch(page, -1) {
		if code := get(handler, http.MethodGet, testListen, reference[1], nil).Code; code != http.StatusOK {
			t.Errorf("index.html references %s, which answers %d", reference[1], code)
		}
	}
}

// Everything the page shows comes from the edge and the machine, so it is
// written as text. These are the calls that would parse it as markup instead.
func TestTheScriptNeverParsesDataAsMarkup(t *testing.T) {
	script, err := webFiles.ReadFile("web/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "setAttribute('style'", "setAttribute(\"style\""} {
		if strings.Contains(string(script), forbidden) {
			t.Errorf("app.js uses %s", forbidden)
		}
	}
}

func TestValidateListenAddrAcceptsOnlyLiteralLoopback(t *testing.T) {
	for _, good := range []string{"127.0.0.1:18095", "127.0.0.2:8095", "[::1]:18095"} {
		if err := ValidateListenAddr(good); err != nil {
			t.Errorf("%s rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:18095", ":18095", "localhost:18095", "192.168.1.10:18095", "[::]:18095", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:http"} {
		if err := ValidateListenAddr(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestValidateEdgeURLAcceptsOnlyABareLoopbackURL(t *testing.T) {
	parsed, err := ValidateEdgeURL("control", "http://127.0.0.1:18091/")
	if err != nil || parsed.String() != "http://127.0.0.1:18091" {
		t.Fatalf("ValidateEdgeURL = %v, %v", parsed, err)
	}
	for _, bad := range []string{
		"https://127.0.0.1:18091", "http://user:pass@127.0.0.1:18091", "http://127.0.0.1:18091/api",
		"http://127.0.0.1:18091?x=1", "http://127.0.0.1:18091#f", "http://localhost:18091", "http://10.0.0.5:18091",
		"http://127.0.0.1", "file:///C:/IA",
	} {
		if _, err := ValidateEdgeURL("control", bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func newActionHandler(t *testing.T, approver approver, executor adminExecutor, peer bool) *handler {
	t.Helper()
	collector, _ := newTestCollector(t, fakeEdge(t, serveInference(inferenceJSON)), &fakeSampler{})
	collector.control = &controller{
		environment: "canary",
		approver:    approver,
		executor:    executor,
		status:      func() *edgeStatus { return rosterStatus("") },
		now:         collector.now,
		log:         func(string, map[string]any) {},
	}
	built, err := NewHandler(collector, testListen)
	if err != nil {
		t.Fatal(err)
	}
	h := built.(*handler)
	h.peer = func(*http.Request) bool { return peer }
	return h
}

// pageHeaders is what the page's own fetch sends.
func pageHeaders() map[string]string {
	return map[string]string{
		"Origin":               "http://" + testListen,
		"Sec-Fetch-Site":       "same-origin",
		"X-CIA-Monitor-Action": "1",
		"Content-Type":         "application/json",
	}
}

func postAction(h http.Handler, method string, headers map[string]string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+testListen+actionPath, strings.NewReader(body))
	request.Host = testListen
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder
}

func TestActionStartsAnOperationThatAwaitsConfirmation(t *testing.T) {
	approver := &fakeApprover{decision: approvalGranted}
	executor := &fakeExecutor{}
	h := newActionHandler(t, approver, executor, true)

	recorder := postAction(h, http.MethodPost, pageHeaders(), `{"action":"switch","model":"huge"}`)
	if recorder.Code != http.StatusAccepted || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST = %d %v %s", recorder.Code, recorder.Header(), recorder.Body)
	}
	var response struct {
		Operation operationView `json:"operation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Operation.State != stateAwaitingConfirmation || response.Operation.Model != "huge" {
		t.Fatalf("operation = %+v", response.Operation)
	}
	waitForOperation(t, h.collector.control, stateSucceeded)
	if requests := executor.seen(); len(requests) != 1 || requests[0].ModelID != "huge" {
		t.Fatalf("pipe requests = %+v", requests)
	}
}

// A form or script on another site, or a page that is not this one, is
// refused before the controller is consulted.
func TestActionAcceptsOnlyThisPagesOwnRequests(t *testing.T) {
	for name, mutate := range map[string]func(map[string]string){
		"no Origin":                 func(h map[string]string) { delete(h, "Origin") },
		"another Origin":            func(h map[string]string) { h["Origin"] = "http://attacker.example" },
		"a null Origin":             func(h map[string]string) { h["Origin"] = "null" },
		"another port":              func(h map[string]string) { h["Origin"] = "http://127.0.0.1:18096" },
		"a cross-site fetch":        func(h map[string]string) { h["Sec-Fetch-Site"] = "cross-site" },
		"a same-site fetch":         func(h map[string]string) { h["Sec-Fetch-Site"] = "same-site" },
		"no custom header":          func(h map[string]string) { delete(h, "X-CIA-Monitor-Action") },
		"a form content type":       func(h map[string]string) { h["Content-Type"] = "application/x-www-form-urlencoded" },
		"a plain-text content type": func(h map[string]string) { h["Content-Type"] = "text/plain" },
	} {
		approver := &fakeApprover{decision: approvalGranted}
		h := newActionHandler(t, approver, &fakeExecutor{}, true)
		headers := pageHeaders()
		mutate(headers)
		recorder := postAction(h, http.MethodPost, headers, `{"action":"switch","model":"huge"}`)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "cross_origin") {
			t.Errorf("%s: %d %s, want 403 cross_origin", name, recorder.Code, recorder.Body)
		}
		if len(approver.seen()) != 0 {
			t.Errorf("%s: a refused request reached the confirmation", name)
		}
	}
}

func TestActionRefusesAConnectionFromAnotherUser(t *testing.T) {
	approver := &fakeApprover{decision: approvalGranted}
	h := newActionHandler(t, approver, &fakeExecutor{}, false)
	recorder := postAction(h, http.MethodPost, pageHeaders(), `{"action":"switch","model":"huge"}`)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "peer_not_allowed") {
		t.Fatalf("another user's connection = %d %s", recorder.Code, recorder.Body)
	}
	if len(approver.seen()) != 0 {
		t.Fatal("another user's request reached the confirmation")
	}
}

func TestActionRejectsMalformedBodies(t *testing.T) {
	for name, body := range map[string]string{
		"an unknown field":  `{"action":"switch","model":"huge","force":true}`,
		"trailing data":     `{"action":"switch","model":"huge"} {"action":"unload","model":"huge"}`,
		"trailing brace":    `{"action":"switch","model":"huge"}}`,
		"trailing bracket":  `{"action":"switch","model":"huge"}]`,
		"oversized suffix":  `{"action":"switch","model":"huge"}` + strings.Repeat(" ", maxActionBody*2) + "x",
		"an array":          `[]`,
		"not JSON":          `action=switch&model=huge`,
		"an oversized body": `{"action":"switch","model":"` + strings.Repeat("h", 2048) + `"}`,
	} {
		h := newActionHandler(t, &fakeApprover{decision: approvalGranted}, &fakeExecutor{}, true)
		if recorder := postAction(h, http.MethodPost, pageHeaders(), body); recorder.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, recorder.Code, recorder.Body)
		}
	}
}

func TestActionRouteIsPostOnly(t *testing.T) {
	h := newActionHandler(t, &fakeApprover{}, &fakeExecutor{}, true)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		recorder := postAction(h, method, pageHeaders(), "")
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "POST" {
			t.Errorf("%s %s = %d Allow=%q", method, actionPath, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
	// The Host check still comes first.
	request := httptest.NewRequest(http.MethodPost, "http://attacker.example:18095"+actionPath, strings.NewReader(`{}`))
	request.Host = "attacker.example:18095"
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || recorder.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("rebinding host = %d", recorder.Code)
	}
}
