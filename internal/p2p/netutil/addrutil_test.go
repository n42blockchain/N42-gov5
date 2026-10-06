package netutil

import (
	"net"
	"testing"
)

func TestAddrIP(t *testing.T) {
	tests := []struct {
		name string
		addr net.Addr
		want string
	}{
		{"ipaddr", &net.IPAddr{IP: net.ParseIP("1.2.3.4")}, "1.2.3.4"},
		{"tcpaddr", &net.TCPAddr{IP: net.ParseIP("5.6.7.8"), Port: 30303}, "5.6.7.8"},
		{"udpaddr", &net.UDPAddr{IP: net.ParseIP("9.9.9.9"), Port: 1}, "9.9.9.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AddrIP(tt.addr)
			if got == nil || got.String() != tt.want {
				t.Fatalf("AddrIP(%v) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

type unknownAddr struct{}

func (unknownAddr) Network() string { return "unknown" }
func (unknownAddr) String() string  { return "unknown" }

func TestAddrIPUnknownType(t *testing.T) {
	if got := AddrIP(unknownAddr{}); got != nil {
		t.Fatalf("AddrIP(unknownAddr{}) = %v, want nil", got)
	}
}
