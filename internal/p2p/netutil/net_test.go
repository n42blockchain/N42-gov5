package netutil

import (
	"net"
	"testing"
)

func TestParseNetlist(t *testing.T) {
	l, err := ParseNetlist(" 10.0.0.0/8, 192.168.0.0/16 \n,, 2001:db8::/32")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*l) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(*l))
	}
	if !l.Contains(net.ParseIP("10.1.2.3")) {
		t.Error("expected to contain 10.1.2.3")
	}
	if !l.Contains(net.ParseIP("192.168.5.5")) {
		t.Error("expected to contain 192.168.5.5")
	}
	if l.Contains(net.ParseIP("8.8.8.8")) {
		t.Error("did not expect to contain 8.8.8.8")
	}
}

func TestParseNetlistInvalid(t *testing.T) {
	if _, err := ParseNetlist("not-a-cidr"); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestNetlistNilContains(t *testing.T) {
	var l *Netlist
	if l.Contains(net.ParseIP("1.1.1.1")) {
		t.Error("nil Netlist should never contain anything")
	}
}

func TestNetlistMarshalTOML(t *testing.T) {
	l, err := ParseNetlist("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	got := l.MarshalTOML().([]string)
	if len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("unexpected marshal result: %v", got)
	}
}

func TestNetlistUnmarshalTOML(t *testing.T) {
	var l Netlist
	fn := func(v interface{}) error {
		ptr := v.(*[]string)
		*ptr = []string{"10.0.0.0/8", "192.168.0.0/16"}
		return nil
	}
	if err := l.UnmarshalTOML(fn); err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(l))
	}
}

func TestNetlistUnmarshalTOMLError(t *testing.T) {
	var l Netlist
	errFn := func(v interface{}) error {
		return net.InvalidAddrError("bad")
	}
	if err := l.UnmarshalTOML(errFn); err == nil {
		t.Fatal("expected error from fn")
	}

	badCIDR := func(v interface{}) error {
		ptr := v.(*[]string)
		*ptr = []string{"not-a-cidr"}
		return nil
	}
	if err := l.UnmarshalTOML(badCIDR); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestNetlistAddPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid CIDR")
		}
	}()
	var l Netlist
	l.Add("garbage")
}

func TestIsLAN(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.1.2.3", true},
		{"172.16.0.5", true},
		{"192.168.1.1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
	}
	for _, c := range cases {
		got := IsLAN(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("IsLAN(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestIsSpecialNetwork(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"224.0.0.1", true}, // multicast
		{"192.0.2.1", true}, // TEST-NET-1
		{"255.255.255.255", true},
		{"2001:db8::1", true},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
	}
	for _, c := range cases {
		got := IsSpecialNetwork(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("IsSpecialNetwork(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestCheckRelayIP(t *testing.T) {
	tests := []struct {
		sender, addr string
		wantErr      bool
	}{
		{"1.2.3.4", "1.2.3.4", false},
		{"1.2.3.4", "0.0.0.0", true},               // unspecified
		{"1.2.3.4", "255.255.255.255", true},        // special
		{"1.2.3.4", "127.0.0.1", true},              // loopback from non-loopback
		{"127.0.0.1", "127.0.0.1", false},           // loopback ok from loopback
		{"1.2.3.4", "10.0.0.1", true},                // LAN from WAN
		{"10.0.0.2", "10.0.0.1", false},              // LAN from LAN
		{"1.2.3.4", "8.8.8.8", false},
	}
	for _, tt := range tests {
		err := CheckRelayIP(net.ParseIP(tt.sender), net.ParseIP(tt.addr))
		if (err != nil) != tt.wantErr {
			t.Errorf("CheckRelayIP(%s, %s) = %v, wantErr %v", tt.sender, tt.addr, err, tt.wantErr)
		}
	}
}

func TestCheckRelayIPInvalidLength(t *testing.T) {
	err := CheckRelayIP(net.ParseIP("1.2.3.4"), net.IP{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for malformed IP")
	}
}

func TestSameNet(t *testing.T) {
	tests := []struct {
		bits       uint
		a, b       string
		want       bool
	}{
		{24, "192.168.1.1", "192.168.1.200", true},
		{24, "192.168.1.1", "192.168.2.1", false},
		{32, "192.168.1.1", "192.168.1.1", true},
		{0, "1.2.3.4", "9.9.9.9", true},
		{16, "2001:db8::1", "2001:db8::2", true},
		{32, "2001:db8::1", "2001:aaaa::2", false},
	}
	for _, tt := range tests {
		got := SameNet(tt.bits, net.ParseIP(tt.a), net.ParseIP(tt.b))
		if got != tt.want {
			t.Errorf("SameNet(%d, %s, %s) = %v, want %v", tt.bits, tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSameNetMixedFamily(t *testing.T) {
	if SameNet(8, net.ParseIP("1.2.3.4"), net.ParseIP("::1")) {
		t.Error("expected false for mixed address families")
	}
}

func TestDistinctNetSet(t *testing.T) {
	s := &DistinctNetSet{Subnet: 24, Limit: 2}

	ips := []net.IP{
		net.ParseIP("192.168.1.1"),
		net.ParseIP("192.168.1.2"),
		net.ParseIP("192.168.1.3"), // exceeds limit of 2 in /24
	}
	if !s.Add(ips[0]) {
		t.Error("expected first add to succeed")
	}
	if !s.Add(ips[1]) {
		t.Error("expected second add to succeed")
	}
	if s.Add(ips[2]) {
		t.Error("expected third add to fail (limit exceeded)")
	}
	if s.Len() != 2 {
		t.Errorf("Len() = %d, want 2", s.Len())
	}
	// Contains tracks subnet occupancy, not individual IPs, so any IP in the
	// same /24 (including the rejected one) reports as contained.
	if !s.Contains(ips[0]) {
		t.Error("expected set to contain ips[0]'s subnet")
	}
	if !s.Contains(ips[2]) {
		t.Error("expected set to contain ips[2]'s subnet (shared with ips[0]/ips[1])")
	}
	if s.Contains(net.ParseIP("10.0.0.1")) {
		t.Error("expected set to not contain an unrelated subnet")
	}

	s.Remove(ips[0])
	if s.Len() != 1 {
		t.Errorf("Len() after remove = %d, want 1", s.Len())
	}
	// The subnet still has one member (ips[1]) so it remains "contained".
	if !s.Contains(ips[0]) {
		t.Error("expected subnet to still be contained after partial remove")
	}

	s.Remove(ips[1])
	if s.Contains(ips[0]) {
		t.Error("expected subnet entry removed once last member is gone")
	}

	// Removing an IP not in the set should be a no-op.
	s.Remove(net.ParseIP("10.0.0.1"))

	str := s.String()
	if str == "" {
		t.Error("expected non-empty String()")
	}
}

func TestDistinctNetSetIPv6(t *testing.T) {
	s := &DistinctNetSet{Subnet: 32, Limit: 1}
	a := net.ParseIP("2001:db8::1")
	b := net.ParseIP("2001:db8::2")
	if !s.Add(a) {
		t.Fatal("expected add to succeed")
	}
	if s.Add(b) {
		t.Fatal("expected second add in same /32 to fail")
	}
	if s.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", s.Len())
	}
}
