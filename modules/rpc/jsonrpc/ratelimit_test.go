package jsonrpc

import (
	"net/http"
	"testing"
)

// TestClientIP_TrustedProxyGating is the regression guard for the X-Forwarded-For
// spoofing fix: the forwarded header is honored ONLY from a trusted proxy peer,
// and even then the right-most non-proxy hop is taken (a spoofed left-most entry
// is ignored). With no trusted proxies, the peer address is always used.
func TestClientIP_TrustedProxyGating(t *testing.T) {
	req := func(remote, xff, xri string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if xri != "" {
			r.Header.Set("X-Real-IP", xri)
		}
		return r
	}

	trusted := ParseCIDRs([]string{"10.0.0.0/8", "192.168.1.1"})

	cases := []struct {
		name    string
		r       *http.Request
		noTrust bool // true → call with no trusted proxies
		want    string
	}{
		{
			name:    "no trusted proxies → ignore XFF, use RemoteAddr",
			r:       req("203.0.113.7:443", "1.2.3.4", "5.6.7.8"),
			noTrust: true,
			want:    "203.0.113.7",
		},
		{
			name: "untrusted peer → ignore XFF even when configured",
			r:    req("203.0.113.7:443", "1.2.3.4", ""),
			want: "203.0.113.7",
		},
		{
			name: "trusted peer → take right-most non-proxy XFF hop",
			r:    req("10.1.2.3:443", "1.2.3.4, 9.9.9.9, 10.4.4.4", ""),
			want: "9.9.9.9", // 10.4.4.4 is a trusted hop, skipped; 9.9.9.9 is the client
		},
		{
			name: "trusted peer, all XFF hops trusted → fall through to peer",
			r:    req("10.1.2.3:443", "10.4.4.4, 192.168.1.1", ""),
			want: "10.1.2.3",
		},
		{
			name: "trusted peer, X-Real-IP fallback when no XFF",
			r:    req("192.168.1.1:443", "", "8.8.8.8"),
			want: "8.8.8.8",
		},
		{
			name: "spoofed left-most XFF from trusted peer does not win",
			r:    req("10.1.2.3:443", "6.6.6.6, 7.7.7.7", ""),
			want: "7.7.7.7", // right-most untrusted hop, not the spoofable 6.6.6.6
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nets := trusted
			if tc.noTrust {
				nets = nil
			}
			got := ClientIP(tc.r, nets)
			if got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
