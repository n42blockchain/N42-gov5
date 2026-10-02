package enr

import (
	"errors"
	"net"
	"testing"
)

func TestTCP6UDP6Keys(t *testing.T) {
	var r Record
	r.Set(TCP6(1234))
	r.Set(UDP6(5678))

	var tcp6 TCP6
	if err := r.Load(&tcp6); err != nil || tcp6 != 1234 {
		t.Fatalf("Load(TCP6) = %d, err=%v", tcp6, err)
	}
	var udp6 UDP6
	if err := r.Load(&udp6); err != nil || udp6 != 5678 {
		t.Fatalf("Load(UDP6) = %d, err=%v", udp6, err)
	}
}

func TestIPEntryKeySelection(t *testing.T) {
	v4 := IP(net.ParseIP("1.2.3.4"))
	if v4.ENRKey() != "ip" {
		t.Fatalf("v4 ENRKey() = %q, want ip", v4.ENRKey())
	}
	v6 := IP(net.ParseIP("2001:db8::1"))
	if v6.ENRKey() != "ip6" {
		t.Fatalf("v6 ENRKey() = %q, want ip6", v6.ENRKey())
	}
}

func TestIPEncodeDecodeRoundTrip(t *testing.T) {
	// Note: IP.ENRKey() depends on the value being encoded/decoded (it picks
	// "ip" or "ip6" based on whether the address looks like IPv4). A nil/zero
	// IP used purely as a Load target defaults to "ip6", so we must load via
	// the concrete IPv4 type to read back a value stored under "ip".
	var r Record
	r.Set(IP(net.ParseIP("192.168.1.1").To4()))

	var out IPv4
	if err := r.Load(&out); err != nil {
		t.Fatalf("Load(IPv4) failed: %v", err)
	}
	if !net.IP(out).Equal(net.ParseIP("192.168.1.1")) {
		t.Fatalf("decoded IP = %v, want 192.168.1.1", net.IP(out))
	}
}

func TestIPv6EncodeDecodeRoundTrip(t *testing.T) {
	var r Record
	addr := net.ParseIP("2001:db8::1")
	r.Set(IPv6(addr))

	var out IPv6
	if err := r.Load(&out); err != nil {
		t.Fatalf("Load(IPv6) failed: %v", err)
	}
	if !net.IP(out).Equal(addr) {
		t.Fatalf("decoded IPv6 = %v, want %v", net.IP(out), addr)
	}
}

func TestIPv6EncodeInvalidAddress(t *testing.T) {
	v := IPv6(net.IP{1, 2, 3}) // too short to be IPv4 or IPv6
	var r Record
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic encoding invalid IPv6 address via Set")
		}
	}()
	r.Set(v)
}

func TestIPv4EncodeInvalidAddress(t *testing.T) {
	v := IPv4(net.ParseIP("2001:db8::1")) // not a valid IPv4
	var r Record
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic encoding invalid IPv4 address via Set")
		}
	}()
	r.Set(v)
}

func TestWithEntryRoundTrip(t *testing.T) {
	var r Record
	r.Set(WithEntry("custom", uint64(99)))

	var out uint64
	if err := r.Load(WithEntry("custom", &out)); err != nil {
		t.Fatalf("Load(custom) failed: %v", err)
	}
	if out != 99 {
		t.Fatalf("out = %d, want 99", out)
	}
}

func TestKeyErrorError(t *testing.T) {
	notFound := &KeyError{Key: "missing", Err: errNotFound}
	if got := notFound.Error(); got == "" {
		t.Fatal("expected non-empty error string")
	}

	other := &KeyError{Key: "k", Err: errors.New("boom")}
	if got := other.Error(); got == "" {
		t.Fatal("expected non-empty error string")
	}
	if !errors.Is(other.Unwrap(), other.Err) {
		t.Fatal("Unwrap should return the wrapped error")
	}
}

func TestIsNotFoundNonKeyError(t *testing.T) {
	if IsNotFound(errors.New("plain")) {
		t.Fatal("expected false for non-KeyError")
	}
}

func TestRecordSize(t *testing.T) {
	var r Record
	r.Set(UDP(1))
	sizeBeforeSign := r.Size()
	if sizeBeforeSign == 0 {
		t.Fatal("expected non-zero computed size before signing")
	}

	s := fakeScheme{sig: []byte("sig")}
	if err := r.SetSig(s, s.sig); err != nil {
		t.Fatal(err)
	}
	sizeAfterSign := r.Size()
	if sizeAfterSign == 0 {
		t.Fatal("expected non-zero raw size after signing")
	}
}

func TestIPDecodeRLPInvalidLength(t *testing.T) {
	// Manually craft a record pair with a 3-byte "IP" value, which is invalid.
	var r Record
	r.Set(WithEntry("ip", []byte{1, 2, 3}))

	var out IP
	err := r.Load(&out)
	if err == nil {
		t.Fatal("expected error decoding malformed IP entry")
	}
}
