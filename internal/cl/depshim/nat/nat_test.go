//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package nat

import (
	"net"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		spec    string
		wantNil bool
		wantErr bool
		wantIP  string
	}{
		{"", true, false, ""},
		{"none", true, false, ""},
		{"None", true, false, ""},
		{"extip:1.2.3.4", false, false, "1.2.3.4"},
		{"EXTIP:10.0.0.5", false, false, "10.0.0.5"},
		{"extip: 8.8.8.8 ", false, false, "8.8.8.8"},
		{"extip:not-an-ip", false, true, ""},
		{"stun", false, true, ""},
		{"upnp", false, true, ""},
		{"pmp:192.168.1.1", false, true, ""},
		{"garbage", false, true, ""},
	}
	for _, tc := range cases {
		iface, err := Parse(tc.spec)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): expected error, got nil", tc.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", tc.spec, err)
			continue
		}
		if tc.wantNil {
			if iface != nil {
				t.Errorf("Parse(%q): expected nil interface", tc.spec)
			}
			continue
		}
		if iface == nil {
			t.Errorf("Parse(%q): expected non-nil interface", tc.spec)
			continue
		}
		ip, err := iface.ExternalIP()
		if err != nil {
			t.Errorf("Parse(%q): ExternalIP err %v", tc.spec, err)
			continue
		}
		if !ip.Equal(net.ParseIP(tc.wantIP)) {
			t.Errorf("Parse(%q): ExternalIP = %v, want %v", tc.spec, ip, tc.wantIP)
		}
	}
}
