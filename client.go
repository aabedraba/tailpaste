package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// peerTimeout bounds a single request to a peer. A peer that is awake answers in
// milliseconds; 5s is a tolerable ceiling for one that is asleep, and short
// enough that the iOS shortcut does not appear to hang.
const peerTimeout = 5 * time.Second

// directClient reaches a peer over the host's own network stack — which means
// the connection the Tailscale GUI app holds, and so only works while that app
// is logged into the tailnet the peer is on. The daemon prefers tsnetClient
// instead; see sendClip.
func directClient() *http.Client {
	return &http.Client{Timeout: peerTimeout}
}

// peerURL accepts any of: "mac-b", "mac-b.tailnet.ts.net", "100.101.102.103",
// "mac-b:9000", or a full "https://mac-b.tailnet.ts.net" (which is what you use
// when fronting the daemon with `tailscale serve`).
func peerURL(cfg *Config, peer, path, query string) string {
	base := peer
	if !strings.Contains(base, "://") {
		if _, _, err := net.SplitHostPort(base); err != nil {
			// No explicit port — JoinHostPort also brackets IPv6 literals.
			base = net.JoinHostPort(base, strconv.Itoa(cfg.Port))
		}
		base = "http://" + base
	}
	url := strings.TrimSuffix(base, "/") + path
	if query != "" {
		url += "?" + query
	}
	return url
}

func postClip(cfg *Config, client *http.Client, peer string, body []byte, fanout bool) error {
	query := ""
	if fanout {
		query = "fanout=1"
	}
	req, err := http.NewRequest(http.MethodPost, peerURL(cfg, peer, "/clip", query), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func getClip(cfg *Config, client *http.Client, peer string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, peerURL(cfg, peer, "/clip", ""), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, cfg.MaxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// errNoDaemon means the local daemon did not answer at all, as opposed to
// answering with a failure. Only the former is worth retrying directly.
var errNoDaemon = errors.New("no local daemon")

// viaDaemon asks the local daemon to talk to a peer on this process's behalf.
//
// A tsnet state directory has a single writer, so the daemon owns the node and a
// short-lived CLI process cannot dial through it. Delegating is what keeps
// `tailpaste push` — and the Raycast commands, which shell out to it — working
// over the daemon's tailnet rather than whichever one the GUI app is on.
func viaDaemon(cfg *Config, method, path, query string, body []byte) ([]byte, error) {
	target := fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Port, path)
	if query != "" {
		target += "?" + query
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	// Allow more than peerTimeout: the daemon is itself waiting on the peer, so
	// this must not be the deadline that fires first.
	client := &http.Client{Timeout: peerTimeout + 3*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoDaemon, err)
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(io.LimitReader(resp.Body, cfg.MaxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("via daemon: %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// delegates reports whether a request for this peer should go through the local
// daemon. Only configured peers qualify, matching the check the daemon itself
// applies — an ad-hoc peer name falls through to a direct dial instead of
// earning a round trip that is certain to be refused.
func delegates(cfg *Config, peer string) bool {
	return cfg.Tsnet.Enabled && isConfiguredPeer(cfg, peer)
}

func isConfiguredPeer(cfg *Config, peer string) bool {
	for _, p := range cfg.Peers {
		if p == peer {
			return true
		}
	}
	return false
}

// sendClip delivers a clip to one peer, preferring the local daemon and falling
// back to a direct dial. The fallback keeps push working with no daemon running,
// which is how the foreground `tailpaste daemon` workflow and the tests use it.
func sendClip(cfg *Config, peer string, body []byte, fanout bool) error {
	if delegates(cfg, peer) {
		query := "peer=" + url.QueryEscape(peer)
		if fanout {
			query += "&fanout=1"
		}
		_, err := viaDaemon(cfg, http.MethodPost, "/relay", query, body)
		if err == nil {
			return nil
		}
		// A daemon that answered with an error has already reached the peer, or
		// failed for a reason a direct dial would hit too.
		if !errors.Is(err, errNoDaemon) {
			return err
		}
	}
	return postClip(cfg, directClient(), peer, body, fanout)
}

// fetchClip is sendClip's mirror: read one peer's clipboard, through the daemon
// when it is there.
func fetchClip(cfg *Config, peer string) ([]byte, error) {
	if delegates(cfg, peer) {
		body, err := viaDaemon(cfg, http.MethodGet, "/fetch", "peer="+url.QueryEscape(peer), nil)
		if err == nil {
			return body, nil
		}
		if !errors.Is(err, errNoDaemon) {
			return nil, err
		}
	}
	return getClip(cfg, directClient(), peer)
}

// push sends this machine's clipboard to a peer.
func push(cfg *Config, peer string, fanout bool) error {
	body, err := readClipboard()
	if err != nil {
		return err
	}
	if err := sendClip(cfg, peer, body, fanout); err != nil {
		return fmt.Errorf("push to %s: %w", peer, err)
	}
	fmt.Printf("pushed %d bytes to %s\n", len(body), peer)
	return nil
}

// pull fetches a peer's clipboard into this machine's clipboard.
func pull(cfg *Config, peer string) error {
	body, err := fetchClip(cfg, peer)
	if err != nil {
		return fmt.Errorf("pull from %s: %w", peer, err)
	}
	if err := writeClipboard(body); err != nil {
		return err
	}
	fmt.Printf("pulled %d bytes from %s\n", len(body), peer)
	return nil
}

// defaultPeer is the first configured peer, which is all you need in the
// common two-machine setup.
func defaultPeer(cfg *Config) (string, error) {
	if len(cfg.Peers) == 0 {
		path, _ := configPath()
		return "", fmt.Errorf("no peers configured — add one to %s", path)
	}
	return cfg.Peers[0], nil
}
