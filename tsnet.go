package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// tsnetUpTimeout bounds how long we wait for the node to come up. The daemon
// does not block on this — it only decides when to give up reporting the node's
// name — but `tailpaste login` does, and a browser login needs a human.
const tsnetUpTimeout = 3 * time.Minute

// newTsnetServer prepares the node without bringing it up. Nothing here talks to
// the network, so it is safe to call before Tailscale or the network is ready.
func newTsnetServer(cfg *Config) (*tsnet.Server, error) {
	dir, err := cfg.Tsnet.stateDir()
	if err != nil {
		return nil, err
	}
	// The node's WireGuard keys live here, so keep it as private as the config.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	return &tsnet.Server{
		Hostname: cfg.Tsnet.hostname(),
		Dir:      dir,
		AuthKey:  cfg.Tsnet.AuthKey, // empty falls back to TS_AUTHKEY
		// Logf is left nil: the backend's logs are verbose enough to bury the
		// request log that is this daemon's main debugging surface. UserLogf is
		// the one that carries the login URL, so it goes to the normal log.
		UserLogf: log.Printf,
	}, nil
}

// tsnetHasState reports whether this machine has already authenticated a node.
// The installer uses it to decide whether a login is still needed.
func tsnetHasState(cfg *Config) bool {
	dir, err := cfg.Tsnet.stateDir()
	if err != nil {
		return false
	}
	info, err := os.Stat(dir + "/tailscaled.state")
	return err == nil && info.Size() > 0
}

// runLogin authenticates this machine's node and enables tsnet in the config.
// It runs in the foreground because an interactive login prints a URL that
// somebody has to open.
func runLogin(cfg *Config) error {
	if !cfg.Tsnet.Enabled {
		// Record the intent first: even if the login below is interrupted, a
		// retry then picks up the same settings.
		cfg.Tsnet.Enabled = true
	}
	if cfg.Tsnet.Hostname == "" {
		// Pin the derived name now, so a later change of the machine's name
		// cannot silently register a second node.
		cfg.Tsnet.Hostname = cfg.Tsnet.hostname()
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}

	srv, err := newTsnetServer(cfg)
	if err != nil {
		return err
	}
	// Route the login URL to stderr rather than the log, so it is visible when
	// this is run by hand.
	srv.UserLogf = func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, strings.TrimSuffix(format, "\n")+"\n", args...)
	}
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "joining the tailnet as %q\n", cfg.Tsnet.Hostname)

	ctx, cancel := context.WithTimeout(context.Background(), tsnetUpTimeout)
	defer cancel()

	status, err := srv.Up(ctx)
	if err != nil {
		return fmt.Errorf("joining the tailnet as %q: %w (if the daemon is running it holds "+
			"the state directory — stop it with `launchctl bootout gui/$(id -u)/com.abdallah.tailpaste` "+
			"and try again)", cfg.Tsnet.Hostname, err)
	}

	name := tsnetDNSName(status)
	fmt.Printf("joined as %s\n", name)
	fmt.Printf("\nPoint your other machines and your iOS shortcut at this name:\n  %s:%d\n", name, cfg.Port)
	fmt.Printf("\nThis node stays on this tailnet no matter which profile the Tailscale app is switched to.\n")
	return nil
}

// logTsnetNode waits for the node to come up and records the name peers have to
// target. It is the only place a running daemon reports that name, so it doubles
// as the message that tells you a login is still outstanding.
func logTsnetNode(srv *tsnet.Server, cfg *Config) {
	ctx, cancel := context.WithTimeout(context.Background(), tsnetUpTimeout)
	defer cancel()

	status, err := srv.Up(ctx)
	if err != nil {
		log.Printf("tailnet node %q is not up: %v — run `tailpaste login`",
			cfg.Tsnet.hostname(), err)
		return
	}
	log.Printf("tailnet node up as %s:%d", tsnetDNSName(status), cfg.Port)
}

// tsnetDNSName is the node's MagicDNS name, which is what peers and the iOS
// shortcut have to target. It differs from the name the Tailscale GUI app
// registers, because this is a separate node.
func tsnetDNSName(status *ipnstate.Status) string {
	if status == nil || status.Self == nil || status.Self.DNSName == "" {
		return "(unknown)"
	}
	return strings.TrimSuffix(status.Self.DNSName, ".")
}

// tsnetClient dials peers through this daemon's own node instead of the host's
// network stack. This is the half that makes outbound pushes independent of the
// GUI app's active profile.
func tsnetClient(srv *tsnet.Server) *http.Client {
	client := srv.HTTPClient()
	client.Timeout = peerTimeout
	return client
}
