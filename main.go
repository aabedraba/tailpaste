// Command tailpaste shares clipboard text between machines over a Tailscale
// tailnet.
//
// Tailscale already provides encryption, NAT traversal and stable addressing,
// so this is deliberately a thin pipe on top of it: one binary that either
// receives (daemon) or sends (push/pull), and an HTTP API small enough to drive
// entirely from curl or an iOS Shortcut.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	defaultPort     = 8787
	defaultMaxBytes = 1 << 20 // 1 MiB
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "tailpaste: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}

	command, rest := args[0], args[1:]
	switch command {
	case "help", "-h", "--help":
		usage()
		return nil
	case "peers":
		// Runs before loadConfig so it works on a machine with no config yet.
		return listPeers()
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	switch command {
	case "init":
		// loadConfig above created the file if it was missing.
		return showConfig(cfg)

	case "daemon":
		return runDaemon(cfg)

	case "login":
		// An explicit hostname is worth passing when two machines report the
		// same name, which is common enough with default macOS names.
		if len(rest) > 0 {
			cfg.Tsnet.Hostname = rest[0]
		}
		return runLogin(cfg)

	case "push":
		peer, fanout := parsePushArgs(rest)
		if peer == "" {
			if peer, err = defaultPeer(cfg); err != nil {
				return err
			}
		}
		return push(cfg, peer, fanout)

	case "pull":
		peer := ""
		if len(rest) > 0 {
			peer = rest[0]
		}
		if peer == "" {
			if peer, err = defaultPeer(cfg); err != nil {
				return err
			}
		}
		return pull(cfg, peer)

	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func parsePushArgs(args []string) (peer string, fanout bool) {
	for _, a := range args {
		if a == "--fanout" {
			fanout = true
			continue
		}
		if peer == "" {
			peer = a
		}
	}
	return peer, fanout
}

// showConfig prints what this machine is configured with. Its main job during
// setup is to show you the token you need to copy to the other Mac.
func showConfig(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	fmt.Printf("config  %s\n", path)
	fmt.Printf("token   %s\n", cfg.Token)
	fmt.Printf("port    %d\n", cfg.Port)
	if len(cfg.Peers) == 0 {
		fmt.Printf("peers   (none yet — add your other Mac)\n")
	} else {
		fmt.Printf("peers   %s\n", strings.Join(cfg.Peers, ", "))
	}

	switch {
	case !cfg.Tsnet.Enabled:
		fmt.Printf("tsnet   off — the daemon rides on the Tailscale app's tailnet, so\n")
		fmt.Printf("        switching that app to another profile takes it offline.\n")
		fmt.Printf("        Run `tailpaste login` to give it a node of its own.\n")
	case !tsnetHasState(cfg):
		fmt.Printf("tsnet   %s (not authenticated yet — run `tailpaste login`)\n", cfg.Tsnet.hostname())
	default:
		fmt.Printf("tsnet   %s (authenticated)\n", cfg.Tsnet.hostname())
	}
	return nil
}

// listPeers is a convenience for filling in the config file. The daemon itself
// never shells out to Tailscale, so it keeps working if the CLI is missing.
func listPeers() error {
	bin, err := tailscaleBinary()
	if err != nil {
		return err
	}
	out, err := exec.Command(bin, "status", "--json").Output()
	if err != nil {
		return fmt.Errorf("tailscale status: %w", err)
	}

	var status struct {
		Peer map[string]struct {
			DNSName string `json:"DNSName"`
			OS      string `json:"OS"`
			Online  bool   `json:"Online"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return err
	}

	var lines []string
	for _, p := range status.Peer {
		state := "offline"
		if p.Online {
			state = "online"
		}
		lines = append(lines, fmt.Sprintf("%-40s %-10s %s",
			strings.TrimSuffix(p.DNSName, "."), p.OS, state))
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

func tailscaleBinary() (string, error) {
	if path, err := exec.LookPath("tailscale"); err == nil {
		return path, nil
	}
	candidates := []string{
		"/usr/local/bin/tailscale",
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("could not find the tailscale CLI")
}

func usage() {
	path, _ := configPath()
	fmt.Fprintf(os.Stderr, `tailpaste — share clipboard text over Tailscale

Usage:
  tailpaste init               Create the config if missing, then show it.
  tailpaste login [name]       Join the daemon to a tailnet as a node of its
                               own, so it keeps working when the Tailscale app
                               is switched to another profile.
  tailpaste daemon             Receive clips. Run this on every machine.
  tailpaste push [peer]        Send this clipboard to a peer.
       --fanout                ...and have that peer relay it onward.
  tailpaste pull [peer]        Fetch a peer's clipboard into this one.
  tailpaste peers              List tailnet machines, to fill in the config.

With no peer argument, the first entry in "peers" is used.

HTTP API (this is the contract an iOS Shortcut targets):
  POST /clip            body is raw text/plain; sets this machine's clipboard
  POST /clip?fanout=1   ...and relays once to every configured peer
  GET  /clip            returns this machine's clipboard as text/plain
  GET  /health          liveness check; the only unauthenticated route

  These two serve this machine's own CLI, which cannot share the daemon's
  tailnet node, and accept only peers named in the config:
  POST /relay?peer=X    forward a clip to one peer; clipboard untouched
  GET  /fetch?peer=X    return one peer's clipboard

  Everything but /health needs:  Authorization: Bearer <token>
  and must originate from a tailnet address.

Config: %s
`, path)
}
