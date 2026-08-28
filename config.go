package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config lives at ~/.config/tailpaste/config.json. Override the location with
// TAILPASTE_CONFIG, which is what makes it possible to run two daemons on one
// machine while testing.
type Config struct {
	Port     int      `json:"port"`
	Token    string   `json:"token"`
	Peers    []string `json:"peers"`
	MaxBytes int64    `json:"max_bytes"`
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

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	// 0600: the token is the only thing standing between a local process and
	// your clipboard.
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "created %s — add your peers to it\n", path)
	return cfg, nil
}
