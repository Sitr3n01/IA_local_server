//go:build windows

// cia-console hosts a WebView2 control that renders the operator console
// frontend. This file wires the real Win32 window, the WebView2 environment,
// and the Bridge (bridge.go) together. Per ADR 0018 this is a Phase A spike:
// it proves control 1 (native confirmation for every mutation, see Bridge
// and win32Approver below) and control 2 (environment sanitization, see
// env_windows.go) are real, wired properties - not a finished console. It
// deliberately never performs a model load; its opt-in E2E harness validates
// host/page integration, security containment, and Sprint 9 visual sanity in a
// real interactive WebView2 session.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"

	"github.com/sitr3n/local-ai-provider/internal/mcpadmin"
	"github.com/sitr3n/local-ai-provider/internal/mcpserver"
)

const (
	consoleVersion = "0.1.0-spike"
	windowTitle    = "CIA Local AI Console (spike)"

	// virtualHostName is the host the frontend is served from. It is not a
	// real, resolvable domain - WebView2's virtual host mapping intercepts
	// it locally (SetVirtualHostNameToFolderMapping below) so the frontend
	// is served from disk with no listening socket and no real DNS lookup,
	// matching ADR 0018's "no port opens" requirement.
	//
	// The suffix is `.invalid`, reserved by RFC 2606 and guaranteed never to
	// resolve, rather than `.localhost`, which Chromium special-cases with
	// built-in loopback resolution. Both were measured to work here - the
	// blank-window bug this spike chased was the controller visibility below,
	// not the host name - so this is a deliberate choice of the TLD that
	// cannot collide with a browser's own resolution rules, not a fix.
	virtualHostName = "cia-console.invalid"

	// diagnoseMarker prefixes the one-shot load self-check's report so it can
	// be claimed off the bridge's message channel before the bridge parses
	// it. diagnosePrefix is the same value as a JS string literal.
	diagnoseMarker = "cia-console-diagnose:"
	diagnosePrefix = `"cia-console-diagnose:"`

	// renderMarker prefixes a second, deliberately delayed report: what the
	// document looks like once the page's own status query has resolved and
	// React has re-rendered.
	//
	// The load self-check above is sampled the instant the bridge reply
	// arrives, which is strictly before the page can have rendered anything
	// from it - so its textLength describes the loading state and says
	// nothing about whether the data was usable. That is not a hypothetical
	// distinction: a payload can arrive complete, correlated, and ok=true and
	// still fail the page's own schema validation, leaving an error card on
	// screen while every host-side signal reports success. Only a sample
	// taken after the render settles tells those two apart.
	renderMarker = "cia-console-render:"
	renderPrefix = `"cia-console-render:"`

	// visualMarker prefixes Sprint 9's layout-geometry report. It is still a
	// diagnostic-only message under CIA_CONSOLE_DIAGNOSE, and it carries counts
	// and rectangles only - never page text, credentials, or a screenshot.
	visualMarker = "cia-console-visual:"
	visualPrefix = `"cia-console-visual:"`

	// navProbeMarker prefixes the message the page posts *after* deliberately
	// attempting to navigate somewhere it is not allowed to go. Receiving it
	// at all is itself half the evidence: a page that had actually navigated
	// away would be gone, and could not post anything. The other half is the
	// guard's own refusal counter, which the host reads and logs on receipt.
	navProbeMarker = "cia-console-navprobe:"
	navProbePrefix = `"cia-console-navprobe:"`

	// navProbeTarget is where the probe tries to go. `.invalid` is reserved
	// by RFC 2606 and resolves nowhere, so if the guard ever failed to cancel
	// this the navigation would fail on its own rather than actually
	// reaching a third party - the probe cannot become a beacon.
	navProbeTarget = "https://navigation-guard-probe.invalid/"
	// The same value as a JS string literal, spliced into the diagnose
	// script below. Kept adjacent to navProbeTarget so the two cannot drift.
	navProbeTargetJS = `"https://navigation-guard-probe.invalid/"`
)

// explicitBrowserArguments is what this host passes to
// CreateCoreWebView2EnvironmentWithOptions. It is deliberately a fixed,
// source-controlled value - even though it is empty for this spike - rather
// than anything derived from the ambient environment, because ADR 0018
// control 2 exists precisely because WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS
// must never be honoured from an inherited process environment.
var explicitBrowserArguments = []string{}

var errNoCredential = errors.New("cia-console does not hold an administrative credential")

// hostChromium is the single console window's WebView2 control. A spike with
// exactly one always-visible top-level window does not need anything more
// elaborate than a package-level reference for the WndProc's WM_SIZE handler
// to resize it through.
var hostChromium *edge.Chromium

func main() {
	runtime.LockOSThread()
	log.SetFlags(0)
	log.SetPrefix("cia-console: ")

	if err := verifyControl2(); err != nil {
		log.Fatalf("refusing to start: %v", err)
	}

	controlConfig, err := mcpserver.ConfigFromEnv()
	if err != nil {
		log.Fatalf("read control configuration: %v", err)
	}
	statusClient, err := mcpserver.NewControlClient(controlConfig, consoleVersion)
	if err != nil {
		log.Fatalf("build status client: %v", err)
	}

	environment := strings.ToLower(strings.TrimSpace(os.Getenv("CIA_ENVIRONMENT")))
	if environment == "" {
		environment = "final"
	}
	installRoot := strings.TrimSpace(os.Getenv("CIA_INSTALL_ROOT"))
	adminPipe, adminPipeServer := mcpadmin.AdminPipeForInstallation(environment, installRoot)

	adminClient, err := mcpadmin.NewClient(mcpadmin.Config{
		ControlURL:      controlConfig.ControlURL,
		Timeout:         15 * time.Second,
		AdminPipe:       adminPipe,
		AdminPipeServer: adminPipeServer,
		// The console never holds an administrative credential (ADR 0018):
		// this stub always fails, so the deprecated HTTP fallback fails
		// closed instead of ever reaching Windows Credential Manager. Only
		// the DACL-protected named pipe can actually complete a mutation.
		TokenProvider: mcpadmin.TokenProviderFunc(func(context.Context) (string, error) {
			return "", errNoCredential
		}),
	}, consoleVersion)
	if err != nil {
		log.Fatalf("build administrative client: %v", err)
	}

	window, err := createHostWindow()
	if err != nil {
		log.Fatalf("create host window: %v", err)
	}

	bridge := NewBridge(win32Approver{owner: window}, adminClient, statusClient)

	chromium := edge.NewChromium()
	hostChromium = chromium
	chromium.AdditionalBrowserArgs = explicitBrowserArguments
	chromium.SetErrorCallback(func(err error) {
		log.Printf("webview2 error: %v", err)
	})
	chromium.MessageCallback = func(message string, _ *edge.ICoreWebView2, _ *edge.ICoreWebView2WebMessageReceivedEventArgs) {
		// The load self-check reports on the same channel the bridge uses,
		// so it is claimed here before the bridge ever sees it. It carries
		// counts, never document content.
		if rest, ok := strings.CutPrefix(message, diagnoseMarker); ok {
			log.Printf("[WebView2] load self-check: %s", rest)
			return
		}
		if rest, ok := strings.CutPrefix(message, renderMarker); ok {
			log.Printf("[WebView2] render self-check: %s", rest)
			return
		}
		if rest, ok := strings.CutPrefix(message, visualMarker); ok {
			log.Printf("[WebView2] visual self-check: %s", rest)
			return
		}
		if _, ok := strings.CutPrefix(message, navProbeMarker); ok {
			// The page is still here to send this, which already means the
			// navigation it attempted did not happen. The counters say why.
			installed, refusals, lastURI := navigationGuardStats()
			winInstalled, winRefusals, winLastURI := newWindowGuardStats()
			log.Printf("[WebView2] navigation guard self-check: "+
				`{"installed":%t,"refusals":%d,"lastRefusedURI":%q,"pageSurvived":true,`+
				`"windowGuardInstalled":%t,"windowRefusals":%d,"lastRefusedWindowURI":%q}`,
				installed, refusals, lastURI, winInstalled, winRefusals, winLastURI)
			return
		}
		handleBridgeMessage(bridge, chromium, message)
	}
	chromium.WebResourceRequestedCallback = func(request *edge.ICoreWebView2WebResourceRequest, args *edge.ICoreWebView2WebResourceRequestedEventArgs) {
		containNavigation(chromium, request, args)
	}
	// Navigation completion is observation only - the binding exposes the
	// event but its args type carries no accessor for IsSuccess or
	// WebErrorStatus, so this records that navigation finished, not how.
	// Even that is worth having: a window that renders nothing looks
	// identical whether navigation never started or completed onto an empty
	// document, and those have entirely different causes.
	chromium.NavigationCompletedCallback = func(sender *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
		log.Printf("[WebView2] navigation completed for https://%s/", virtualHostName)

		// Install the navigation guard (navigation_windows.go). This is the
		// first point at which a raw *ICoreWebView2 is reachable at all: the
		// binding keeps its own reference unexported and hands one out only
		// as a callback's sender, so there is no earlier hook. The practical
		// consequence is that the console's own first navigation - the
		// hardcoded Navigate() call below, to the virtual host - is not
		// guarded, and every navigation after it is. That is a boundary
		// worth naming rather than hiding, but it is not a gap: the one
		// unguarded navigation is a constant in this file, not anything the
		// page can influence.
		//
		// installNavigationGuard is idempotent, which matters because this
		// callback fires on every completed navigation, not only the first.
		if err := installNavigationGuard(sender, virtualHostName); err != nil {
			// Loud, because a guard that failed to register is
			// indistinguishable from one that registered and was never
			// needed - both are silent, and both leave the console
			// unprotected. That confusion is exactly what made the previous
			// WebResourceRequested attempt useless for a whole sprint.
			log.Printf("[WebView2] NAVIGATION GUARD NOT INSTALLED: %v", err)
		}

		// One-shot load self-check, gated behind CIA_CONSOLE_DIAGNOSE.
		//
		// Host-injected ExecuteScript is not subject to the page's own
		// Content-Security-Policy, so this discriminates the two causes a
		// blank window otherwise conflates: if this reports back, the
		// document loaded and something stopped the page's own script; if
		// nothing arrives, the document itself never loaded. It reports
		// sizes and counts only - no document content leaves the page.
		//
		// It also exercises the bridge's correlation id round trip end to
		// end: it posts its own {id, kind:"status"} request through
		// window.chrome.webview.postMessage - the same call
		// bridgeTransport.ts makes - and waits (bounded to 10s) for a reply
		// carrying that same id to arrive at window.__ciaConsoleReply,
		// which the frontend bundle's own bridgeTransport module has
		// already installed by the time navigation completes (React has
		// mounted; see the react_mounted assertion in
		// e2e_windows_test.go). Doing this from inside the page - rather
		// than reaching into the WebView2 process from the Go test some
		// other way - is what actually proves data crosses the id
		// correlation, not merely that Bridge.Handle can be called in
		// isolation (bridge_test.go already covers that). The temporary
		// wrap-and-restore of window.__ciaConsoleReply below never drops a
		// reply meant for the page's own in-flight requests: every other
		// id is forwarded to the previous handler unchanged, and the
		// original handler is restored the instant this check settles (by
		// match or by timeout), whichever comes first. No debugging port is
		// opened for any of this; it is the existing postMessage/Eval
		// channel only.
		if os.Getenv("CIA_CONSOLE_DIAGNOSE") == "" {
			return
		}
		chromium.Eval(`(function(){
  function ciaConsoleDiagnoseReport(extra){
    try{
      var r=document.getElementById('root');
      var b=document.body.getBoundingClientRect();
      var cs=getComputedStyle(document.body);
      var first=r&&r.firstElementChild;
      var fr=first?first.getBoundingClientRect():null;
      var payload={
        url: location.href,
        readyState: document.readyState,
        viewport: window.innerWidth+'x'+window.innerHeight,
        bodyRect: Math.round(b.width)+'x'+Math.round(b.height),
        bodyBg: cs.backgroundColor,
        bodyColor: cs.color,
        bodyVisibility: cs.visibility,
        bodyOpacity: cs.opacity,
        scripts: document.scripts.length,
        stylesheets: document.styleSheets.length,
        rootChildren: r ? r.childElementCount : -1,
        firstChild: first ? first.tagName+'.'+(first.className||'') : null,
        firstChildRect: fr ? Math.round(fr.width)+'x'+Math.round(fr.height) : null,
        textLength: document.body.innerText.length
      };
      if (extra) { for (var k in extra) { payload[k] = extra[k]; } }
      window.chrome.webview.postMessage(` + diagnosePrefix + `+JSON.stringify(payload));
    }catch(e){window.chrome.webview.postMessage(` + diagnosePrefix + `+'threw: '+e.message);}
  }

  try {
    var diagId = 'cia-console-diagnose-status-'+Date.now()+'-'+Math.random().toString(36).slice(2);
    var settled = false;
    var previousReply = window.__ciaConsoleReply;

    var timer = setTimeout(function(){
      if (settled) { return; }
      settled = true;
      window.__ciaConsoleReply = previousReply;
      ciaConsoleDiagnoseReport({bridgeStatusRoundTrip:{attempted:true,matched:false,timedOut:true}});
    }, 10000);

    window.__ciaConsoleReply = function(reply){
      if (!settled && reply && reply.id === diagId) {
        settled = true;
        clearTimeout(timer);
        window.__ciaConsoleReply = previousReply;
        var summary = {attempted:true, matched:true, ok: !!(reply && reply.ok)};
        if (reply.status && typeof reply.status === 'object') {
          summary.service = reply.status.service;
          summary.version = reply.status.version;
          summary.ready = reply.status.ready;
          summary.modelsCount = Array.isArray(reply.status.models) ? reply.status.models.length : -1;
          // The keys that actually survived the trip. service/version/ready/
          // modelsCount alone cannot detect field loss on the way here -
          // they are exactly the fields a narrower host-side projection
          // still happens to carry - so the harness compares this list
          // against what the page's schema requires.
          summary.statusKeys = Object.keys(reply.status).sort();
          var firstModel = Array.isArray(reply.status.models) ? reply.status.models[0] : null;
          if (firstModel && typeof firstModel === 'object') {
            summary.modelKeys = Object.keys(firstModel).sort();
          }
          var firstModelStatus = Array.isArray(reply.status.model_statuses) ? reply.status.model_statuses[0] : null;
          if (firstModelStatus && typeof firstModelStatus === 'object') {
            summary.modelStatusKeys = Object.keys(firstModelStatus).sort();
          }
          if (reply.status.capacity && typeof reply.status.capacity === 'object') {
            summary.capacityKeys = Object.keys(reply.status.capacity).sort();
          }
        }
        if (reply.error) { summary.errorCode = reply.error.code; }
        ciaConsoleDiagnoseReport({bridgeStatusRoundTrip: summary});
        // Sampled after the page has had time to re-render from the reply
        // this handler just observed. Counts and lengths only, like the
        // report above - no document content leaves the page. The two counts
        // read the page's own accessibility state rather than private
        // selectors: aria-busy marks a screen still pending, role="alert"
        // marks one that failed, and a screen that is neither has rendered.
        setTimeout(function(){
          try{
            window.chrome.webview.postMessage(` + renderPrefix + ` + JSON.stringify({
              textLength: document.body.innerText.length,
              busyRegions: document.querySelectorAll('[aria-busy="true"]').length,
              alertRegions: document.querySelectorAll('[role="alert"]').length
            }));
          }catch(e){
            window.chrome.webview.postMessage(` + renderPrefix + ` + 'threw: '+e.message);
          }

          // Sprint 9: a visual-layout sanity report, sampled from the settled
          // document before the navigation probes begin. It deliberately
          // reports counts, dimensions, and overflow figures only. Pixel
          // evidence is captured outside the page by the E2E harness.
          try{
            var isVisible = function(el){
              if (!el) { return false; }
              if (el.classList && el.classList.contains('ds-visually-hidden')) { return false; }
              var style = getComputedStyle(el);
              if (style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity) === 0) { return false; }
              var rect = el.getBoundingClientRect();
              return rect.width > 0 && rect.height > 0;
            };
            var visibleCount = function(selector){
              return Array.prototype.filter.call(document.querySelectorAll(selector), isVisible).length;
            };
            var critical = [
              document.querySelector('.app-shell'),
              document.querySelector('.app-shell__main'),
              document.querySelector('.app-shell__content'),
              document.querySelector('nav[aria-label="Primary"]'),
              document.querySelector('main')
            ];
            var invisibleCritical = critical.filter(function(el){ return !isVisible(el); }).length;
            var interactiveTooSmall = Array.prototype.filter.call(
              document.querySelectorAll('button,a,input,select,textarea'),
              function(el){
                if (!isVisible(el)) { return false; }
                var rect = el.getBoundingClientRect();
                return rect.width < 28 || rect.height < 28;
              }
            ).length;
            window.chrome.webview.postMessage(` + visualPrefix + ` + JSON.stringify({
              viewportWidth: window.innerWidth,
              viewportHeight: window.innerHeight,
              documentScrollWidth: document.documentElement.scrollWidth,
              bodyScrollWidth: document.body.scrollWidth,
              horizontalOverflowPx: Math.max(
                0,
                document.documentElement.scrollWidth - window.innerWidth,
                document.body.scrollWidth - window.innerWidth
              ),
              invisibleCritical: invisibleCritical,
              primaryNavButtons: visibleCount('nav[aria-label="Primary"] button'),
              mainHeadings: visibleCount('main h1'),
              overviewSections: visibleCount('.overview-section'),
              visibleButtons: visibleCount('button'),
              interactiveTooSmall: interactiveTooSmall
            }));
          }catch(e){
            window.chrome.webview.postMessage(` + visualPrefix + ` + 'threw: '+e.message);
          }

          // Deliberately try to leave. This is the navigation guard's only
          // real test: the guard is a NavigationStarting handler, and the
          // difference between one that is registered and one that actually
          // fires and cancels cannot be observed any other way. The target
          // resolves nowhere (RFC 2606 .invalid), so a guard that failed
          // would produce a failed navigation rather than contact with a
          // third party.
          try { window.location.href = ` + navProbeTargetJS + `; } catch (e) { /* refused synchronously is also a pass */ }

          // The second egress channel, probed separately. window.open is not
          // a navigation of this window and does not raise NavigationStarting,
          // and no Content-Security-Policy closes it - CSP governs what a
          // document may fetch, embed and execute, not whether the browser may
          // open a window. Only the host's NewWindowRequested refusal does.
          try { window.open(` + navProbeTargetJS + `, '_blank'); } catch (e) { /* refused synchronously is also a pass */ }

          // Reported after the attempt has had time to either be cancelled
          // or to tear this document down. If this arrives, the document is
          // still alive and the navigation did not happen.
          setTimeout(function(){
            try{
              window.chrome.webview.postMessage(` + navProbePrefix + ` + '1');
            }catch(e){}
          }, 900);
        }, 2500);
        return;
      }
      if (typeof previousReply === 'function') { previousReply(reply); }
    };

    window.chrome.webview.postMessage(JSON.stringify({id: diagId, kind: 'status'}));
  } catch (e) {
    ciaConsoleDiagnoseReport({bridgeStatusRoundTrip:{attempted:false,error:e.message}});
  }
})()`)
	}

	// Embed pumps Windows messages itself until the environment and
	// controller are ready (see pkg/edge/chromium.go, Chromium.Embed), so it
	// must run before this function's own message loop starts.
	if ok := chromium.Embed(uintptr(window)); !ok {
		log.Fatal("embed webview2 control failed")
	}

	if err := hardenSettings(chromium); err != nil {
		log.Fatalf("harden webview2 settings: %v", err)
	}

	v3 := chromium.GetICoreWebView2_3()
	if v3 == nil {
		log.Fatal("ICoreWebView2_3 is unavailable; cannot map the virtual host")
	}
	// Resolve and report the mapped folder. A virtual host pointed at a
	// directory that does not exist maps successfully and then serves
	// nothing, which presents as a blank window with no error anywhere -
	// so the path and its existence are operational facts worth stating at
	// startup rather than facts to be guessed at from a white rectangle.
	distDir := frontendDistDir()
	if info, statErr := os.Stat(distDir); statErr != nil {
		log.Printf("[WebView2] frontend directory %q is not readable: %v", distDir, statErr)
	} else if !info.IsDir() {
		log.Printf("[WebView2] frontend path %q is not a directory", distDir)
	} else {
		log.Printf("[WebView2] serving %q at https://%s/", distDir, virtualHostName)
	}
	if err := v3.SetVirtualHostNameToFolderMapping(virtualHostName, distDir, edge.COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_DENY_CORS); err != nil {
		log.Fatalf("map virtual host: %v", err)
	}

	// Registering the filter for every resource context, on every URL,
	// means containNavigation (below) sees the top-level document request
	// too, not only subresources - that is what lets it stand in for
	// cancelling a navigation whose target is not the virtual host.
	chromium.AddWebResourceRequestedFilter("*", edge.COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL)

	chromium.Navigate("https://" + virtualHostName + "/index.html")

	_, _, _ = procShowWindow.Call(uintptr(window), swShow)

	// Make the controller visible. Embed creates and correctly sizes it but
	// leaves ICoreWebView2Controller::IsVisible false, so its child windows
	// exist at the right size and composite nothing: the host window shows
	// its own blank background while the page behind it is fully loaded,
	// styled and mounted. This single call is what made the spike window
	// render; it was isolated by removing each candidate change in turn and
	// re-running the E2E harness, which is also why the harness asserts on
	// child-window visibility rather than only on what the page reports
	// about itself. A page cannot see whether it is being composited.
	if err := chromium.Show(); err != nil {
		log.Fatalf("make the webview2 control visible: %v", err)
	}

	runMessageLoop()
}

// hardenSettings applies the DOM-side half of ADR 0018: no DevTools, no
// default (right-click) context menus, and no browser accelerator keys, so
// the rendered page has no path to its own developer tooling or Chromium
// chrome.
func hardenSettings(chromium *edge.Chromium) error {
	settings, err := chromium.GetSettings()
	if err != nil {
		return fmt.Errorf("get webview2 settings: %w", err)
	}
	if err := settings.PutAreDevToolsEnabled(false); err != nil {
		return fmt.Errorf("disable devtools: %w", err)
	}
	if err := settings.PutAreDefaultContextMenusEnabled(false); err != nil {
		return fmt.Errorf("disable default context menus: %w", err)
	}
	if err := settings.PutAreBrowserAcceleratorKeysEnabled(false); err != nil {
		return fmt.Errorf("disable browser accelerator keys: %w", err)
	}
	return nil
}

// containNavigation refuses the *content* of any resource request whose
// target is not the virtual host. It is NOT this console's navigation
// control - navigation_windows.go is - and this comment says so explicitly
// because for an entire sprint this function was mistaken for one.
//
// It was written to stand in for cancelling a navigation, on the theory that
// registering AddWebResourceRequestedFilter("*", ...CONTEXT_ALL) would put
// the top-level document request through here. Instrumenting it showed the
// callback never fires for a virtual-host resource at all - not for the
// document, not for a subresource - because WebView2 resolves virtual-host
// mappings below the layer WebResourceRequested observes. As navigation
// containment it was inert, which is worse than absent: an inert guard is one
// everybody downstream believes in.
//
// It is kept, deliberately, as a second and independent layer for requests
// that do reach this callback - anything not served by the virtual host
// mapping. Its scope is honestly narrower than it looks, and two other
// controls stand in front of it for that traffic: the page's own
// Content-Security-Policy, and the navigation guard. What is NOT claimed
// here is a measurement: this host has never observed this callback firing,
// for anything, so its effectiveness for off-host requests is argued rather
// than demonstrated. Nothing in ADR 0018's control set depends on it.
func containNavigation(chromium *edge.Chromium, request *edge.ICoreWebView2WebResourceRequest, args *edge.ICoreWebView2WebResourceRequestedEventArgs) {
	uri, err := request.GetUri()
	if err != nil {
		log.Printf("[WebView2] request with an unreadable URI: %v", err)
		return
	}
	parsed, err := url.Parse(uri)
	if err == nil && strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), virtualHostName) {
		log.Printf("[WebView2] allowed %s://%s%s", parsed.Scheme, parsed.Hostname(), parsed.Path)
		return
	}

	// Metadata only, and bounded: the scheme and host of a refused request,
	// never a query string, never a body. That is the same discipline
	// internal/edge applies to its own request log, and it is what makes a
	// containment decision diagnosable instead of invisible.
	refused := "(unparsable)"
	if err == nil {
		refused = parsed.Scheme + "://" + parsed.Hostname()
	}
	log.Printf("[WebView2] refused a request outside the virtual host: %s", refused)

	blocked, respErr := chromium.Environment().CreateWebResourceResponse(nil, 403, "Blocked by CIA Local AI Console", "")
	if respErr != nil {
		log.Printf("[WebView2] could not build the refusal response: %v", respErr)
		return
	}
	_ = args.PutResponse(blocked)
}

// handleBridgeMessage decodes one web message as an Envelope and dispatches
// it via Bridge.HandleMessage - bridge.go's entire decode-validate-dispatch
// pipeline, the only path allowed to reach the Approver, the AdminClient, or
// the StatusClient - then posts the resulting ReplyEnvelope back to the
// page. The 30s bound here is this operation's total budget: Bridge.Handle
// always returns a Result (an error one, if ctx expires before the
// StatusClient/AdminClient call returns) rather than leaving the page
// without a reply to correlate against.
func handleBridgeMessage(bridge *Bridge, chromium *edge.Chromium, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reply := bridge.HandleMessage(ctx, []byte(message))
	postBridgeReply(chromium, reply)
}

// postBridgeReply delivers a ReplyEnvelope back to the page. This binding's
// MessageReceived handler (pkg/edge/chromium.go) unconditionally echoes the
// page's own original message back via PostWebMessageAsString immediately
// after MessageCallback returns, so that channel cannot carry our reply,
// too. Eval (window.chrome.webview.postMessage's counterpart on the host
// side, ExecuteScript) is used instead to invoke a page-side callback with
// the JSON reply - id included - as its argument.
func postBridgeReply(chromium *edge.Chromium, reply ReplyEnvelope) {
	payload, err := json.Marshal(reply)
	if err != nil {
		payload = []byte(`{"ok":false,"error":{"code":"internal_error","message":"failed to encode response"}}`)
	}
	// Logged before the Eval, and deliberately not paired with an "after"
	// line: Eval is fire-and-forget (ExecuteScript takes a completion
	// handler this binding does not wrap), so a line printed after it
	// returns would report a delivery nothing actually observed.
	//
	// payloadBytes earns its place. It is what exposed the status read's
	// silent field loss - cia-edge's 24652-byte snapshot arriving at the
	// page as 16645 after a round-trip through a narrower Go struct - a
	// defect that was otherwise invisible because the reply's own ok flag
	// was true the whole time.
	log.Printf("[WebView2] reply: op=%q ok=%v payloadBytes=%d", reply.Operation, reply.OK, len(payload))
	chromium.Eval("window.__ciaConsoleReply && window.__ciaConsoleReply(" + string(payload) + ")")
}

// frontendDistDir resolves the folder SetVirtualHostNameToFolderMapping
// serves. This spike intentionally does not use go:embed for it (ADR 0018):
// that would require the folder to exist at compile time, which would in
// turn require a Node toolchain on the ubuntu-latest race job that only ever
// builds this package for GOOS=linux, where the WebView2 code below is not
// even compiled.
func frontendDistDir() string {
	if override := strings.TrimSpace(os.Getenv("CIA_CONSOLE_FRONTEND_DIR")); override != "" {
		return override
	}
	exePath, err := os.Executable()
	if err != nil {
		return filepath.Join("frontend", "dist")
	}
	return filepath.Join(filepath.Dir(exePath), "frontend", "dist")
}

// win32Approver is the production Approver: a native MB_OKCANCEL dialog
// owned by the host window, naming the operation and its target. The page
// has no handle to this dialog - it runs on the host's own thread, driven
// directly by a Win32 syscall, not by anything the DOM can reach - so it can
// neither forge an approval, suppress the prompt, nor answer it itself. This
// is the control that stands in for the second factor the credential-free
// bridge does not provide.
type win32Approver struct {
	owner windows.Handle
}

const (
	mbOKCancel    = 0x00000001
	mbIconWarning = 0x00000030
	mbDefButton2  = 0x00000100 // default focus on Cancel, the safer choice
	mbTopMost     = 0x00040000
	idOK          = 1
)

func (a win32Approver) Approve(op Operation) bool {
	target := op.ModelID
	if target == "" {
		target = "(no model)"
	}
	text := fmt.Sprintf(
		"The operator console is asking to run:\r\n\r\n    %s %s\r\n\r\n"+
			"This will be sent to the DACL-protected administrative pipe.\r\nAllow it?",
		strings.ToUpper(op.Kind), target,
	)
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return false
	}
	titlePtr, err := windows.UTF16PtrFromString("CIA Local AI Console - confirm operation")
	if err != nil {
		return false
	}
	ret, _, _ := procMessageBoxW.Call(
		uintptr(a.owner),
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOKCancel|mbIconWarning|mbDefButton2|mbTopMost),
	)
	return ret == idOK
}

// --- Minimal Win32 window hosting -----------------------------------------
//
// cia-tray.exe already has a fuller Win32 message-window implementation in
// internal/trayui, but its types are unexported and it is shaped around a
// notification-area icon and dashboard controls, not a full-size top-level
// window hosting a WebView2 control. This is a small, self-contained
// equivalent scoped to exactly what a single visible host window needs:
// register a window class, create the window, resize the embedded control
// on WM_SIZE, and quit on WM_DESTROY.

type point struct {
	X int32
	Y int32
}

type msgT struct {
	Window  windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   point
	Private uint32
}

type wndClassEx struct {
	Size        uint32
	Style       uint32
	WindowProc  uintptr
	ClassExtra  int32
	WindowExtra int32
	Instance    windows.Handle
	Icon        windows.Handle
	Cursor      windows.Handle
	Background  windows.Handle
	MenuName    *uint16
	ClassName   *uint16
	IconSmall   windows.Handle
}

const (
	wmDestroy = 0x0002
	wmSize    = 0x0005
	wmClose   = 0x0010

	wsOverlappedWindow = 0x00CF0000
	swShow             = 5
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procShowWindow       = user32.NewProc("ShowWindow")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

func createHostWindow() (windows.Handle, error) {
	instance, _, callErr := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return 0, fmt.Errorf("get process module: %w", callErr)
	}
	className, err := windows.UTF16PtrFromString(fmt.Sprintf("CIA.LocalAI.Console.%d", windows.GetCurrentProcessId()))
	if err != nil {
		return 0, err
	}
	title, err := windows.UTF16PtrFromString(windowTitle)
	if err != nil {
		return 0, err
	}

	class := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		WindowProc: windows.NewCallback(windowProc),
		Instance:   windows.Handle(instance),
		ClassName:  className,
	}
	atom, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return 0, fmt.Errorf("register console window class: %w", registerErr)
	}

	window, _, createErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow,
		100, 100, 1280, 860,
		0, 0, instance, 0,
	)
	if window == 0 {
		return 0, fmt.Errorf("create console window: %w", createErr)
	}
	return windows.Handle(window), nil
}

func windowProc(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmSize:
		if hostChromium != nil {
			hostChromium.Resize()
		}
		return 0
	case wmClose:
		_, _, _ = procDestroyWindow.Call(window)
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
	return ret
}

func runMessageLoop() {
	var msg msgT
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) <= 0 {
			return
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
