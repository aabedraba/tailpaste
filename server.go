package main

import (
	"crypto/subtle"
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
			r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBytes)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					http.Error(w, "clipboard too large", http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, "cannot read body", http.StatusBadRequest)
				return
			}
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

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
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
