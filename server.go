package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// Tailscale hands out addresses from the CGNAT range (IPv4) and this ULA prefix
// (IPv6). Rejecting everything else means that even if the daemon is somehow
// exposed on a hostile LAN, only tailnet peers get as far as the token check.
var (
	tailscaleV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

func fromTailnet(remoteAddr string) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return false
	}
	addr := ap.Addr().Unmap()
	return addr.IsLoopback() || tailscaleV4.Contains(addr) || tailscaleV6.Contains(addr)
}

// authorize applies both gates: source address, then bearer token.
func authorize(cfg *Config, r *http.Request) error {
	if !fromTailnet(r.RemoteAddr) {
		return fmt.Errorf("source %s is not on the tailnet", r.RemoteAddr)
	}
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return errors.New("missing bearer token")
	}
	got := []byte(strings.TrimPrefix(header, prefix))
	if subtle.ConstantTimeCompare(got, []byte(cfg.Token)) != 1 {
		return errors.New("bad token")
	}
	return nil
}

func runDaemon(cfg *Config) error {
	addr := fmt.Sprintf(":%d", cfg.Port)

	// peerClient is how this daemon reaches its peers. Plain by default; with
	// tsnet enabled it dials through this daemon's own tailnet node instead, so
	// outbound pushes no longer depend on which profile the Tailscale GUI app
	// happens to be logged into.
	peerClient := directClient()

	// The plain listener keeps the loopback health check, the CLI's delegation
	// path and the GUI app's own tailnet address working exactly as before.
	// Bind broadly rather than to the 100.x address: the daemon must survive
	// starting before Tailscale is up, and Tailscale addresses can change.
	// fromTailnet() is what actually restricts access.
	local, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	listeners := []net.Listener{local}

	if cfg.Tsnet.Enabled {
		srv, err := newTsnetServer(cfg)
		if err != nil {
			return err
		}
		defer srv.Close()

		tsListener, err := srv.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listening on the tailnet as %q: %w", cfg.Tsnet.hostname(), err)
		}
		listeners = append(listeners, tsListener)
		peerClient = tsnetClient(srv)

		// Reported in the background: an unauthenticated or offline node must
		// not stop the daemon from serving on the listeners it already has.
		go logTsnetNode(srv, cfg)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/clip", protect(cfg, handleClip(cfg, peerClient)))
	mux.HandleFunc("/iosclip", protect(cfg, handleIOSClip(cfg, peerClient)))
	// These two exist for this machine's own CLI, which cannot borrow the
	// daemon's tsnet node itself; see viaDaemon.
	mux.HandleFunc("/relay", protect(cfg, handleRelay(cfg, peerClient)))
	mux.HandleFunc("/fetch", protect(cfg, handleFetch(cfg, peerClient)))

	log.Printf("listening on %s (%d listener(s)), %d peer(s) configured",
		addr, len(listeners), len(cfg.Peers))
	return serveAll(listeners, logging(mux))
}

// serveAll runs one server per listener and returns as soon as any of them
// fails, so a broken listener does not leave the daemon half working — launchd
// restarts it rather than leaving it reachable on only one path.
func serveAll(listeners []net.Listener, handler http.Handler) error {
	errs := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func() { errs <- http.Serve(ln, handler) }()
	}
	return <-errs
}

func protect(cfg *Config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := authorize(cfg, r); err != nil {
			log.Printf("denied %s %s from %s: %v", r.Method, r.URL.Path, r.RemoteAddr, err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	fmt.Fprintf(w, "ok %s\n", host)
}

func handleClip(cfg *Config, client *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			body, err := readClipboard()
			if err != nil {
				log.Printf("read clipboard: %v", err)
				http.Error(w, "cannot read clipboard", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write(body)

		case http.MethodPost:
			body, ok := readBody(w, r, cfg.MaxBytes)
			if !ok {
				return
			}
			setClip(cfg, client, w, r, body)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handleIOSClip is /clip's POST behaviour with a JSON envelope. iOS Shortcuts
// can only send a JSON body from some actions, so the text arrives wrapped as
// {"content": "..."} rather than as the raw body.
func handleIOSClip(cfg *Config, client *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, ok := readBody(w, r, cfg.MaxBytes)
		if !ok {
			return
		}
		// A pointer distinguishes {"content": ""} — a deliberate clear — from a
		// body that forgot the field, which is a shortcut wired up wrong.
		var envelope struct {
			Content *string `json:"content"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			http.Error(w, "body must be JSON", http.StatusBadRequest)
			return
		}
		if envelope.Content == nil {
			http.Error(w, `body must have a "content" string`, http.StatusBadRequest)
			return
		}
		setClip(cfg, client, w, r, []byte(*envelope.Content))
	}
}

// handleRelay forwards a clip to one named peer without touching this machine's
// clipboard, and handleFetch reads one peer's clipboard back. Both are here so
// that `tailpaste push`/`pull` can reach peers through the daemon's tailnet node
// rather than the host's network stack.
func handleRelay(cfg *Config, client *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		peer, ok := requestedPeer(cfg, w, r)
		if !ok {
			return
		}
		body, ok := readBody(w, r, cfg.MaxBytes)
		if !ok {
			return
		}
		if err := postClip(cfg, client, peer, body, r.URL.Query().Get("fanout") == "1"); err != nil {
			log.Printf("relay to %s failed: %v", peer, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("relayed %d bytes to %s for %s", len(body), peer, r.RemoteAddr)
		fmt.Fprintln(w, "ok")
	}
}

func handleFetch(cfg *Config, client *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		peer, ok := requestedPeer(cfg, w, r)
		if !ok {
			return
		}
		body, err := getClip(cfg, client, peer)
		if err != nil {
			log.Printf("fetch from %s failed: %v", peer, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("fetched %d bytes from %s for %s", len(body), peer, r.RemoteAddr)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(body)
	}
}

// requestedPeer resolves the ?peer= argument, refusing any name that is not in
// the config. Without that check these two routes would make the daemon an open
// proxy to any address, for anyone holding the token.
func requestedPeer(cfg *Config, w http.ResponseWriter, r *http.Request) (string, bool) {
	peer := r.URL.Query().Get("peer")
	if peer == "" {
		http.Error(w, "missing peer", http.StatusBadRequest)
		return "", false
	}
	if !isConfiguredPeer(cfg, peer) {
		http.Error(w, "peer is not in the config", http.StatusForbidden)
		return "", false
	}
	return peer, true
}

// readBody reads a capped request body, writing the error response itself. The
// bool reports whether the caller should continue.
func readBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "clipboard too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// setClip writes the clipboard and answers the request, relaying to peers when
// fanout=1 was asked for.
func setClip(cfg *Config, client *http.Client, w http.ResponseWriter, r *http.Request, body []byte) {
	if err := writeClipboard(body); err != nil {
		log.Printf("write clipboard: %v", err)
		http.Error(w, "cannot write clipboard", http.StatusInternalServerError)
		return
	}
	log.Printf("set clipboard (%d bytes) from %s", len(body), r.RemoteAddr)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("fanout") == "1" {
		fmt.Fprintf(w, "ok %s\n", relay(cfg, client, body))
	} else {
		fmt.Fprintln(w, "ok")
	}
}

// relay forwards a clip to every configured peer. Forwarded requests always go
// out with fanout=0, so a relay can never trigger another relay — the hop depth
// is capped structurally, with no message IDs or dedup cache needed.
func relay(cfg *Config, client *http.Client, body []byte) string {
	if len(cfg.Peers) == 0 {
		return "(no peers configured)"
	}
	var sent int
	var failures []string
	for _, peer := range cfg.Peers {
		if err := postClip(cfg, client, peer, body, false); err != nil {
			log.Printf("relay to %s failed: %v", peer, err)
			failures = append(failures, fmt.Sprintf("%s: %v", peer, err))
			continue
		}
		sent++
	}
	if len(failures) > 0 {
		return fmt.Sprintf("(relayed %d/%d; %s)", sent, len(cfg.Peers), strings.Join(failures, "; "))
	}
	return fmt.Sprintf("(relayed %d/%d)", sent, len(cfg.Peers))
}

// logging records every request. This log is the main debugging surface once
// the daemon is running headless under launchd.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		query := ""
		if r.URL.RawQuery != "" {
			query = "?" + r.URL.RawQuery
		}
		log.Printf("%s %s%s from %s -> %d", r.Method, r.URL.Path, query, host, rec.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
