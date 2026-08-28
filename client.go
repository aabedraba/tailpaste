package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// One shared client for both CLI commands and daemon relays. A peer that is
// awake answers in milliseconds; 5s is a tolerable ceiling for one that is
// asleep, and short enough that the iOS shortcut does not appear to hang.
var httpClient = &http.Client{Timeout: 5 * time.Second}

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

func postClip(cfg *Config, peer string, body []byte, fanout bool) error {
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

	resp, err := httpClient.Do(req)
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

func getClip(cfg *Config, peer string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, peerURL(cfg, peer, "/clip", ""), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := httpClient.Do(req)
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

// push sends this machine's clipboard to a peer.
func push(cfg *Config, peer string, fanout bool) error {
	body, err := readClipboard()
	if err != nil {
		return err
	}
	if err := postClip(cfg, peer, body, fanout); err != nil {
		return fmt.Errorf("push to %s: %w", peer, err)
	}
	fmt.Printf("pushed %d bytes to %s\n", len(body), peer)
	return nil
}

// pull fetches a peer's clipboard into this machine's clipboard.
func pull(cfg *Config, peer string) error {
	body, err := getClip(cfg, peer)
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
