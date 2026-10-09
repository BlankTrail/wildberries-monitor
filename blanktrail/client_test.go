// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BlankTrail/wildberries-monitor/internal/testutil/fakebt"
)

func newTestClient(t *testing.T) (*Client, *fakebt.Server) {
	t.Helper()
	fake := fakebt.New(t)
	c, err := NewClient(fake.URL(), fake.Key())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, fake
}

func TestDefaultPortSpec_ArmsChallengeBreakerAndOneRequestPerSession(t *testing.T) {
	s := DefaultPortSpec()
	if !s.JSSolver {
		t.Error("JSSolver must default to true: the target is behind a JS challenge")
	}
	if !s.KeepSessions {
		t.Error("KeepSessions must default to true: a port is a session with its own cookie jar")
	}
	// Left unset on purpose, so the open request omits it and the port keeps its
	// own policy. The limit governs everything flowing through the port, not only
	// the requests we issue, so a low limit can starve the port's own challenge
	// handling. Measured against a live target: at 1 every request came back
	// challenged; with the limit lifted the same request returned a full page.
	if s.MaxConcurrent != 0 {
		t.Errorf("MaxConcurrent=%d, want it unset so the port keeps its own policy", s.MaxConcurrent)
	}
	if s.Mode != "db" {
		t.Errorf("Mode=%q, want \"db\": a real profile from the curated database, not a synthetic one", s.Mode)
	}
}

func TestClient_OpenPortSendsSpecAndParsesProfile(t *testing.T) {
	c, fake := newTestClient(t)

	info, err := c.OpenPort(context.Background(), 20001, DefaultPortSpec(),
		Egress{Upstream: "socks5://user:pass@1.2.3.4:1080"})
	if err != nil {
		t.Fatalf("OpenPort: %v", err)
	}
	if info.Port != 20001 {
		t.Errorf("Port=%d, want 20001", info.Port)
	}
	if info.Profile.Name == "" {
		t.Error("Profile.Name is empty; the open response carries current_profile")
	}

	var sent map[string]any
	for _, r := range fake.Requests() {
		if r.Path == "/api/v1/ports/open" {
			if err := json.Unmarshal([]byte(r.Body), &sent); err != nil {
				t.Fatalf("decode recorded open body: %v", err)
			}
		}
	}
	if sent == nil {
		t.Fatal("no request recorded for /api/v1/ports/open")
	}
	for _, key := range []string{"js_solver", "keep_sessions", "h2_spoofing", "spoof_headers"} {
		if v, ok := sent[key].(bool); !ok || !v {
			t.Errorf("open body %s=%v, want true", key, sent[key])
		}
	}
	if _, present := sent["max_concurrent"]; present {
		t.Errorf("open body carries max_concurrent=%v; an unset limit must be omitted so the port keeps its own", sent["max_concurrent"])
	}
	if got, _ := sent["upstream"].(string); got != "socks5://user:pass@1.2.3.4:1080" {
		t.Errorf("open body upstream=%q, want the egress upstream", got)
	}
	if _, present := sent["auto_rotate"]; present {
		t.Error("open body carries auto_rotate; that key was removed from the control API")
	}
}

func TestClient_OpenPortWithGatewayOmitsUpstream(t *testing.T) {
	c, fake := newTestClient(t)

	if _, err := c.OpenPort(context.Background(), 20002, DefaultPortSpec(),
		Egress{Gateway: "vless-nl"}); err != nil {
		t.Fatalf("OpenPort: %v", err)
	}

	var sent map[string]any
	for _, r := range fake.Requests() {
		if r.Path == "/api/v1/ports/open" {
			_ = json.Unmarshal([]byte(r.Body), &sent)
		}
	}
	if got, _ := sent["upstream_gateway"].(string); got != "vless-nl" {
		t.Errorf("upstream_gateway=%q, want \"vless-nl\"", got)
	}
	if _, present := sent["upstream"]; present {
		t.Error("upstream must be absent when the egress is a gateway")
	}
}

func TestClient_SetUpstreamAndRotate(t *testing.T) {
	c, fake := newTestClient(t)
	ctx := context.Background()

	if _, err := c.OpenPort(ctx, 20003, DefaultPortSpec(), Egress{}); err != nil {
		t.Fatalf("OpenPort: %v", err)
	}
	if err := c.SetUpstream(ctx, 20003, "http://9.9.9.9:8080"); err != nil {
		t.Fatalf("SetUpstream: %v", err)
	}
	if got := fake.UpstreamOf(20003); got != "http://9.9.9.9:8080" {
		t.Errorf("fake upstream=%q, want the new one", got)
	}

	p, err := c.RotateProfile(ctx, 20003)
	if err != nil {
		t.Fatalf("RotateProfile: %v", err)
	}
	if p.Name == "" {
		t.Error("RotateProfile returned an empty profile name")
	}
	if n := fake.RotateCount(20003); n != 1 {
		t.Errorf("RotateCount=%d, want 1", n)
	}
}

func TestClient_UnauthorizedIsTyped(t *testing.T) {
	fake := fakebt.New(t)
	c, err := NewClient(fake.URL(), "wrong-key")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// The licence and not health: the live service answers /health with no
	// key at all (200 with none and with a wrong one), and refuses the first
	// call that needs one.
	_, err = c.LicenseStatus(context.Background())
	if err == nil {
		t.Fatal("LicenseStatus with a wrong key returned nil error")
	}
	if !IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false, want true", err)
	}
	var apiErr *APIError
	if !errorsAs(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if apiErr.Message != "authentication required" {
		t.Errorf("Message=%q, want the server's error text", apiErr.Message)
	}
}

func TestClient_SuggestAndClose(t *testing.T) {
	c, fake := newTestClient(t)
	ctx := context.Background()

	p, err := c.SuggestPort(ctx)
	if err != nil {
		t.Fatalf("SuggestPort: %v", err)
	}
	if p < 1 || p > 65535 {
		t.Fatalf("SuggestPort returned %d, want a valid TCP port", p)
	}
	if _, err := c.OpenPort(ctx, p, DefaultPortSpec(), Egress{}); err != nil {
		t.Fatalf("OpenPort: %v", err)
	}
	if err := c.ClosePort(ctx, p); err != nil {
		t.Fatalf("ClosePort: %v", err)
	}
	if len(fake.OpenPorts()) != 0 {
		t.Errorf("OpenPorts=%v, want empty after close", fake.OpenPorts())
	}
}

func TestNewClient_RejectsBadInput(t *testing.T) {
	if _, err := NewClient("", "k"); err == nil {
		t.Error("NewClient with an empty base URL returned nil error")
	}
	if _, err := NewClient("http://", "k"); err == nil {
		t.Error("NewClient with a host-less URL returned nil error")
	}
}

func TestAPIError_MessageFallsBackToBody(t *testing.T) {
	fake := fakebt.New(t)
	fake.FailNext("/api/v1/health", http.StatusInternalServerError, "boom, not json")
	c, _ := NewClient(fake.URL(), fake.Key())

	err := c.Health(context.Background())
	var apiErr *APIError
	if !errorsAs(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if !strings.Contains(apiErr.Message, "boom") {
		t.Errorf("Message=%q, want the raw body when it is not JSON", apiErr.Message)
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

func TestDefaultPortSpec_GivesUpOnADeadExitInSeconds(t *testing.T) {
	// On a cheap list half the addresses do not answer at all. Without a
	// connect timeout of its own the port waited the service's default on each
	// of them, and 500 threads over 15 000 server proxies made three pages a
	// second (09.10.2026). Google Parser asks for five seconds; so does this.
	req := DefaultPortSpec().request(20000, Egress{Upstream: "socks5://10.0.0.1:1080"})
	if req.ConnectTimeout == nil || *req.ConnectTimeout != 5 {
		t.Errorf("connect_timeout_seconds = %v, want 5", req.ConnectTimeout)
	}
}
