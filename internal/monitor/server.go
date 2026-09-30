package monitor

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Every route but one answers GET and HEAD only, and the monitor holds no
// credential that a script running in the page could borrow. The one exception
// is POST /api/actions, which relays a switch or an unload to the edge's
// administrative pipe - and only for this page, in this user's browser, after
// the operator confirms it outside the page (control.go). Everything below
// keeps the page from being framed, fed from another origin, or reached through
// a hostname that is not this machine's.

//go:embed web
var webFiles embed.FS

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// assetTypes is the allowlist of files the page may load, with their types
// fixed here rather than sniffed.
var assetTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
}

type asset struct {
	body        []byte
	contentType string
	etag        string
}

// NewHandler serves the monitor for one listen address. The address fixes the
// Host values accepted, which is what defeats DNS rebinding: a page on another
// site that resolves its own name to 127.0.0.1 still sends its own name.
func NewHandler(collector *Collector, listenAddr string) (http.Handler, error) {
	hosts, err := allowedHosts(listenAddr)
	if err != nil {
		return nil, err
	}
	index, err := loadAsset("web/index.html", "text/html; charset=utf-8")
	if err != nil {
		return nil, err
	}
	assets := make(map[string]asset)
	err = fs.WalkDir(webFiles, "web/assets", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		contentType, ok := assetTypes[path.Ext(name)]
		if !ok {
			return fmt.Errorf("embedded asset %s has no declared content type", name)
		}
		loaded, err := loadAsset(name, contentType)
		if err != nil {
			return err
		}
		assets["/"+strings.TrimPrefix(name, "web/")] = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &handler{collector: collector, hosts: hosts, index: index, assets: assets, peer: verifiedPeer}, nil
}

type handler struct {
	collector *Collector
	hosts     map[string]struct{}
	index     asset
	assets    map[string]asset
	// peer decides whether the connection behind a mutation belongs to this
	// process's user. It is a field so tests can stand in for the TCP table.
	peer func(*http.Request) bool
}

const (
	actionPath    = "/api/actions"
	actionHeader  = "X-CIA-Monitor-Action"
	maxActionBody = 1 << 10
)

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), usb=(), serial=(), hid=()")

	if _, ok := h.hosts[strings.ToLower(r.Host)]; !ok {
		plainError(w, http.StatusForbidden, "this monitor answers only on its loopback address")
		return
	}
	if r.URL.Path == actionPath {
		h.serveAction(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", "GET, HEAD")
		plainError(w, http.StatusMethodNotAllowed, "this route is read-only")
		return
	}

	switch r.URL.Path {
	case "/":
		h.serveAsset(w, r, h.index)
	case "/api/snapshot":
		h.serveSnapshot(w)
	default:
		if loaded, ok := h.assets[r.URL.Path]; ok {
			h.serveAsset(w, r, loaded)
			return
		}
		plainError(w, http.StatusNotFound, "not found")
	}
}

// serveAction admits a mutation in this order, cheapest refusal first: the
// method, the request's shape and origin, the connection's owner, the body.
// Nothing reaches the controller until every one of them has passed.
func (h *handler) serveAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		actionFailure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !sameOriginAction(r) {
		actionFailure(w, http.StatusForbidden, "cross_origin")
		return
	}
	if !h.peer(r) {
		actionFailure(w, http.StatusForbidden, "peer_not_allowed")
		return
	}
	var request struct {
		Action string `json:"action"`
		Model  string `json:"model"`
		Source string `json:"source"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxActionBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.More() {
		actionFailure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var operation operationView
	var failure *actionError
	if request.Action == actionStopSource {
		// A stop names a discovered source and never a model or a process.
		if request.Model != "" {
			actionFailure(w, http.StatusBadRequest, "invalid_request")
			return
		}
		operation, failure = h.collector.control.startStop(request.Source)
	} else {
		if request.Source != "" {
			actionFailure(w, http.StatusBadRequest, "invalid_request")
			return
		}
		operation, failure = h.collector.control.start(request.Action, request.Model)
	}
	if failure != nil {
		actionFailure(w, failure.Status, failure.Code)
		return
	}
	writeActionJSON(w, http.StatusAccepted, map[string]any{"operation": operation})
}

// sameOriginAction accepts only what this page's own script sends. A form on
// another site cannot set the custom header or a JSON content type without a
// CORS preflight, which this server never answers; a script there that tried
// anyway would carry its own Origin. Browsers also report the fetch's site,
// and anything but same-origin is refused.
func sameOriginAction(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Origin"), "http://"+r.Host) {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	if r.Header.Get(actionHeader) != "1" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// verifiedPeer asks the kernel which process holds the client end of this
// connection and whether it runs as this process's user.
func verifiedPeer(r *http.Request) bool {
	client, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	server, err := netip.ParseAddrPort(local.String())
	if err != nil {
		return false
	}
	same, err := sameUserPeer(client, server)
	return err == nil && same
}

func actionFailure(w http.ResponseWriter, status int, code string) {
	writeActionJSON(w, status, map[string]string{"error": code})
}

func writeActionJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		plainError(w, http.StatusInternalServerError, "encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (h *handler) serveSnapshot(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	body := h.collector.Snapshot()
	if body == nil {
		w.Header().Set("Retry-After", "1")
		plainError(w, http.StatusServiceUnavailable, "the first sample is still being taken")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveAsset revalidates on every load. The files are compiled in, so a new
// binary is a new page, and an ETag makes the revalidation a 304 otherwise.
func (h *handler) serveAsset(w http.ResponseWriter, r *http.Request, loaded asset) {
	header := w.Header()
	header.Set("Cache-Control", "no-cache")
	header.Set("ETag", loaded.etag)
	if match := r.Header.Get("If-None-Match"); match != "" && match == loaded.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set("Content-Type", loaded.contentType)
	header.Set("Content-Length", strconv.Itoa(len(loaded.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(loaded.body)
}

func loadAsset(name, contentType string) (asset, error) {
	body, err := webFiles.ReadFile(name)
	if err != nil {
		return asset{}, err
	}
	sum := sha256.Sum256(body)
	return asset{body: body, contentType: contentType, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}, nil
}

func plainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n"))
}

// ValidateListenAddr accepts a literal loopback IP and an explicit port. A
// hostname is refused because what it resolves to is decided elsewhere, and a
// wildcard or LAN address would publish the page to the network.
func ValidateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must be a literal loopback IP such as 127.0.0.1", addr)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("listen address %q must name a port between 1 and 65535", addr)
	}
	return nil
}

// ValidateEdgeURL accepts a plain-HTTP loopback URL with an explicit port and
// nothing else: no credentials, path, query or fragment. The control plane is
// loopback-only, so anything else is a misconfiguration worth failing on.
func ValidateEdgeURL(label, raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", label, raw, err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("%s %q must be a bare http://127.0.0.1:port URL", label, raw)
	}
	if err := ValidateListenAddr(parsed.Host); err != nil {
		return nil, fmt.Errorf("%s %q must point at a loopback IP with a port", label, raw)
	}
	parsed.Path = ""
	return parsed, nil
}

func allowedHosts(listenAddr string) (map[string]struct{}, error) {
	if err := ValidateListenAddr(listenAddr); err != nil {
		return nil, err
	}
	host, port, _ := net.SplitHostPort(listenAddr)
	ip := net.ParseIP(host)
	hosts := map[string]struct{}{
		net.JoinHostPort(ip.String(), port): {},
	}
	// localhost is the one name accepted: browsers resolve it to loopback
	// themselves, so no DNS answer can point it elsewhere. It is accepted only
	// for the two addresses it can mean.
	if ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback) {
		hosts[net.JoinHostPort("localhost", port)] = struct{}{}
	}
	return hosts, nil
}

// Server wraps the handler with timeouts sized for a page that polls once a
// second and never uploads anything.
func Server(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
