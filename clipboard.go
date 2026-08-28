package main

import (
	"bytes"
	"fmt"
	"os/exec"
)

// readClipboard and writeClipboard are vars rather than plain funcs so tests can
// swap in a fake without needing a real pasteboard.
//
// Clipboard bytes are passed straight between exec and the HTTP body. Never
// route them through shell command substitution — $(...) strips trailing
// newlines and would silently corrupt content.

var readClipboard = func() ([]byte, error) {
	// -Prefer txt asks for the plain-text flavour rather than RTF.
	out, err := exec.Command("pbpaste", "-Prefer", "txt").Output()
	if err != nil {
		return nil, fmt.Errorf("pbpaste: %w", err)
	}
	return out, nil
}

var writeClipboard = func(b []byte) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = bytes.NewReader(b)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pbcopy: %w", err)
	}
	return nil
}
