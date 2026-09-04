package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config lives at ~/.config/tailpaste/config.json. Override the location with
// TAILPASTE_CONFIG, which is what makes it possible to run two daemons on one
// machine while testing.
type Config struct {
	Port     int      `json:"port"`
	Token    string   `json:"token"`
	Peers    []string `json:"peers"`
	MaxBytes int64    `json:"max_bytes"`
	Tsnet    Tsnet    `json:"tsnet"`
}

// Tsnet makes the daemon join a tailnet as a node of its own instead of
// borrowing the connection the Tailscale GUI app holds. The GUI app can only be
// logged into one tailnet at a time, so switching between a personal and a work
// profile otherwise takes the daemon off the tailnet its peers are on. With a
// node of its own the daemon stays reachable through either profile, and it is
// unaffected by ACLs on the tailnet the GUI app happens to be using.
//
// Off by default: a node needs authenticating once before it can serve, which
// `tailpaste login` does.
type Tsnet struct {
	Enabled  bool   `json:"enabled"`
	Hostname string `json:"hostname,omitempty"`
	StateDir string `json:"state_dir,omitempty"`
	// AuthKey is optional. Without one, `tailpaste login` prints a URL to
	// authenticate in a browser.
	AuthKey string `json:"auth_key,omitempty"`
}

func configPath() (string, error) {
	if p := os.Getenv("TAILPASTE_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "tailpaste", "config.json"), nil
}

// loadConfig reads the config file, creating it with a fresh random token if it
// does not exist yet.
func loadConfig() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return createConfig(path)
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Port == 0 {
		cfg.Port = defaultPort
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("%s has an empty token", path)
	}
	return &cfg, nil
}

// hostname is the name this daemon's own tailnet node registers under. It is
// deliberately distinct from the machine's Tailscale name, because the GUI app's
// node and this one are two separate nodes on (usually) two different tailnets.
func (t Tsnet) hostname() string {
	if t.Hostname != "" {
		return t.Hostname
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "tailpaste"
	}
	return "tailpaste-" + sanitizeHostname(host)
}

// sanitizeHostname reduces a machine name to what Tailscale accepts as a node
// name: lowercase letters, digits and single hyphens.
func sanitizeHostname(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, ".local"))
	var b strings.Builder
	lastHyphen := true // leading hyphens are not allowed either
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// stateDir holds the node's identity and its WireGuard keys. It sits beside the
// config so that both pieces of per-machine state live in one place.
func (t Tsnet) stateDir() (string, error) {
	if t.StateDir != "" {
		return t.StateDir, nil
	}
	path, err := configPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "tsnet"), nil
}

// saveConfig writes the config back, which `tailpaste login` needs so that the
// daemon it hands over to knows tsnet is now authenticated and enabled.
func saveConfig(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	return writeConfig(path, cfg)
}

func writeConfig(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the token is the only thing standing between a local process and
	// your clipboard.
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func createConfig(path string) (*Config, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}

	cfg := &Config{
		Port:     defaultPort,
		Token:    hex.EncodeToString(token),
		Peers:    []string{},
		MaxBytes: defaultMaxBytes,
	}

	if err := writeConfig(path, cfg); err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "created %s — add your peers to it\n", path)
	return cfg, nil
}
