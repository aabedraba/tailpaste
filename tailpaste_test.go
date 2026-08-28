package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFromTailnet(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"100.64.0.1:1234", true},          // first CGNAT address
		{"100.127.255.254:1234", true},     // deep inside the range
		{"127.0.0.1:1234", true},           // loopback, for local testing
		{"[::1]:1234", true},               // loopback v6
		{"[fd7a:115c:a1e0::1]:1234", true}, // tailscale v6
		{"192.168.1.50:1234", false},       // plain LAN — must be rejected
		{"10.0.0.5:1234", false},           // private, but not tailscale
		{"100.63.255.255:1234", false},     // one below the CGNAT range
		{"100.128.0.0:1234", false},        // one above the CGNAT range
		{"8.8.8.8:1234", false},            // public internet
		{"garbage", false},                 // unparseable
	}
	for _, c := range cases {
		if got := fromTailnet(c.addr); got != c.want {
			t.Errorf("fromTailnet(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestAuthorize(t *testing.T) {
	cfg := &Config{Token: "sekrit"}
	request := func(remote, auth string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/clip", nil)
		r.RemoteAddr = remote
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}

	if err := authorize(cfg, request("100.64.0.1:1", "Bearer sekrit")); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
	for name, r := range map[string]*http.Request{
		"off-tailnet source": request("192.168.1.2:1", "Bearer sekrit"),
		"no header":          request("100.64.0.1:1", ""),
		"wrong token":        request("100.64.0.1:1", "Bearer nope"),
		"not bearer":         request("100.64.0.1:1", "Basic sekrit"),
		"token as prefix":    request("100.64.0.1:1", "Bearer sekritXX"),
	} {
		if err := authorize(cfg, r); err == nil {
			t.Errorf("%s: expected rejection, got none", name)
		}
	}
}

func TestPeerURL(t *testing.T) {
	cfg := &Config{Port: 8787}
	cases := []struct{ peer, want string }{
		{"mac-b", "http://mac-b:8787/clip"},
		{"mac-b.tailnet.ts.net", "http://mac-b.tailnet.ts.net:8787/clip"},
		{"mac-b:9000", "http://mac-b:9000/clip"},
		{"100.101.102.103", "http://100.101.102.103:8787/clip"},
		{"fd7a:115c:a1e0::1", "http://[fd7a:115c:a1e0::1]:8787/clip"},
		// A full URL is passed through, which is how you point at
		// `tailscale serve` for HTTPS.
		{"https://mac-b.tailnet.ts.net", "https://mac-b.tailnet.ts.net/clip"},
		{"https://mac-b.tailnet.ts.net/", "https://mac-b.tailnet.ts.net/clip"},
	}
	for _, c := range cases {
		if got := peerURL(cfg, c.peer, "/clip", ""); got != c.want {
			t.Errorf("peerURL(%q) = %q, want %q", c.peer, got, c.want)
		}
	}
	if got := peerURL(cfg, "mac-b", "/clip", "fanout=1"); !strings.HasSuffix(got, "?fanout=1") {
		t.Errorf("query not appended: %q", got)
	}
}

// A relayed clip must never carry fanout=1, or two mutually-peered machines
// would bounce it forever.
func TestRelayDoesNotReFanout(t *testing.T) {
	var gotQuery string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
	}))
	defer peer.Close()

	cfg := &Config{Token: "t", Port: 8787, Peers: []string{peer.URL}}
	if summary := relay(cfg, []byte("hi")); !strings.Contains(summary, "1/1") {
		t.Errorf("relay summary = %q, want a 1/1 success", summary)
	}
	if strings.Contains(gotQuery, "fanout=1") {
		t.Errorf("relayed request carried fanout=1 (query %q) — this would loop", gotQuery)
	}
}
