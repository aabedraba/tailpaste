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
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/clip", protect(cfg, handleClip(cfg)))
	mux.HandleFunc("/iosclip", protect(cfg, handleIOSClip(cfg)))

	// Bind broadly rather than to the 100.x address: the daemon must survive
	// starting before Tailscale is up, and Tailscale addresses can change.
	// fromTailnet() is what actually restricts access.
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("listening on %s, %d peer(s) configured", addr, len(cfg.Peers))
	return http.ListenAndServe(addr, logging(mux))
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

func handleClip(cfg *Config) http.HandlerFunc {
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
			setClip(cfg, w, r, body)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handleIOSClip is /clip's POST behaviour with a JSON envelope. iOS Shortcuts
// can only send a JSON body from some actions, so the text arrives wrapped as
// {"content": "..."} rather than as the raw body.
func handleIOSClip(cfg *Config) http.HandlerFunc {
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
		setClip(cfg, w, r, []byte(*envelope.Content))
	}
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
func setClip(cfg *Config, w http.ResponseWriter, r *http.Request, body []byte) {
	if err := writeClipboard(body); err != nil {
		log.Printf("write clipboard: %v", err)
		http.Error(w, "cannot write clipboard", http.StatusInternalServerError)
		return
	}
	log.Printf("set clipboard (%d bytes) from %s", len(body), r.RemoteAddr)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("fanout") == "1" {
		fmt.Fprintf(w, "ok %s\n", relay(cfg, body))
	} else {
		fmt.Fprintln(w, "ok")
	}
}

// relay forwards a clip to every configured peer. Forwarded requests always go
// out with fanout=0, so a relay can never trigger another relay — the hop depth
// is capped structurally, with no message IDs or dedup cache needed.
func relay(cfg *Config, body []byte) string {
	if len(cfg.Peers) == 0 {
		return "(no peers configured)"
	}
	var sent int
	var failures []string
	for _, peer := range cfg.Peers {
		if err := postClip(cfg, peer, body, false); err != nil {
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
