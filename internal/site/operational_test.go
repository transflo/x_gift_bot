package site

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xgift/internal/checkout"
)

func TestNormalizeAnnouncement(t *testing.T) {
	cases := []struct {
		name    string
		in      announcement
		wantErr bool
		want    string
	}{
		{name: "blank disabled is allowed", in: announcement{Enabled: false}, want: "info"},
		{name: "blank enabled is not", in: announcement{Enabled: true, Text: "   "}, wantErr: true},
		{name: "unknown level falls back", in: announcement{Enabled: true, Text: "hi", Level: "loud"}, want: "info"},
		{name: "newlines are rejected", in: announcement{Enabled: true, Text: "a\nb"}, wantErr: true},
		{name: "tabs are rejected", in: announcement{Enabled: true, Text: "a\tb"}, wantErr: true},
		{name: "control characters are rejected", in: announcement{Enabled: true, Text: "a\x07b"}, wantErr: true},
		{name: "a long announcement is rejected", in: announcement{Enabled: true, Text: strings.Repeat("好", 201)}, wantErr: true},
		{name: "200 characters are allowed", in: announcement{Enabled: true, Text: strings.Repeat("好", 200)}, want: "info"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeAnnouncement(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Level != tc.want {
				t.Errorf("level = %q, want %q", got.Level, tc.want)
			}
		})
	}
}

// The banner is rendered on public pages, and the operator may only toggle it
// off rather than retype it; the text has to survive that round trip.
func TestNormalizeAnnouncementKeepsTextWhenDisabled(t *testing.T) {
	got, err := normalizeAnnouncement(announcement{Enabled: false, Text: "维护中", Level: "critical"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "维护中" || got.Level != "critical" {
		t.Errorf("got %+v, want the text and level preserved", got)
	}
}

func TestPruneCutoff(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC).Unix()
	if got := pruneCutoff(now, 30); got != want {
		t.Errorf("pruneCutoff = %d, want %d", got, want)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{name: "socket peer only", remoteAddr: "172.22.0.1:54321", want: "172.22.0.1"},
		{
			// The whole point: inside a container the socket peer is the proxy,
			// so the forwarded address is the only real one available.
			name:       "real ip header wins",
			remoteAddr: "172.22.0.1:54321",
			headers:    map[string]string{"X-Real-IP": "203.0.113.5"},
			want:       "203.0.113.5",
		},
		{
			name:       "forwarded for is the fallback",
			remoteAddr: "172.22.0.1:54321",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7, 10.0.0.1"},
			want:       "198.51.100.7",
		},
		{
			// A proxy that forwards a header it did not overwrite must not be
			// able to inject a non-address into the log.
			name:       "malformed header falls back",
			remoteAddr: "172.22.0.1:54321",
			headers:    map[string]string{"X-Real-IP": "not-an-ip"},
			want:       "172.22.0.1",
		},
		{
			name:       "real ip beats forwarded for",
			remoteAddr: "172.22.0.1:54321",
			headers:    map[string]string{"X-Real-IP": "203.0.113.5", "X-Forwarded-For": "198.51.100.7"},
			want:       "203.0.113.5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			for key, value := range tc.headers {
				r.Header.Set(key, value)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnlogged(t *testing.T) {
	for _, path := range []string{"/livez", "/healthz", "/favicon.ico", "/_next/static/chunk.js", "/_next.__PAGE__.txt", "/__next._tree.txt"} {
		if !unlogged(path) {
			t.Errorf("%s should be excluded from the access log", path)
		}
	}
	for _, path := range []string{"/", "/redeem/", "/api/redeem", "/api/announcement"} {
		if unlogged(path) {
			t.Errorf("%s should be recorded", path)
		}
	}
}

func TestAccessLevel(t *testing.T) {
	cases := map[int]string{200: "info", 302: "info", 403: "warn", 429: "warn", 503: "error"}
	for status, want := range cases {
		if got := accessLevel(status); got != want {
			t.Errorf("accessLevel(%d) = %q, want %q", status, got, want)
		}
	}
}

// A published link ages out on its own, so the state the admin list shows has
// to be derived rather than read: nothing writes a row when a window closes.
func TestDecorate(t *testing.T) {
	now := time.Now().Unix()
	ttl := int64(checkout.PublicLinkTTL / time.Second)
	cases := []struct {
		name string
		in   codeRow
		want string
	}{
		{name: "no link yet", in: codeRow{Status: "processing"}, want: linkNone},
		{name: "live link", in: codeRow{Status: "processing", StripeURL: "https://checkout.stripe.com/c/pay/cs_live_x", LinkCreated: now, LinkState: linkWaiting}, want: linkWaiting},
		{name: "aged out", in: codeRow{Status: "processing", StripeURL: "https://checkout.stripe.com/c/pay/cs_live_x", LinkCreated: now - ttl - 1, LinkState: linkWaiting}, want: linkExpired},
		{name: "paid wins over everything", in: codeRow{Status: "succeeded", StripeURL: "https://checkout.stripe.com/c/pay/cs_live_x", LinkCreated: now - ttl - 1, LinkState: linkWaiting}, want: linkPaid},
		{name: "declined is sticky", in: codeRow{Status: "processing", StripeURL: "https://checkout.stripe.com/c/pay/cs_live_x", LinkCreated: now - ttl - 1, LinkState: linkDeclined}, want: linkDeclined},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.in
			decorate(&row)
			if row.LinkState != tc.want {
				t.Errorf("link state = %q, want %q", row.LinkState, tc.want)
			}
		})
	}
}

func TestLinkExpires(t *testing.T) {
	if got := linkExpires(&codeRow{}); got != 0 {
		t.Errorf("an order with no link should report no expiry, got %d", got)
	}
	created := time.Now().Unix()
	want := created + int64(checkout.PublicLinkTTL/time.Second)
	if got := linkExpires(&codeRow{LinkCreated: created}); got != want {
		t.Errorf("linkExpires = %d, want %d", got, want)
	}
}
