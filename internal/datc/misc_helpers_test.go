package datc

import "testing"

func TestHuman(t *testing.T) {
	cases := []struct {
		n    uint64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1_000, "1K"},
		{1_500, "2K"},
		{999_999, "1000K"},
		{1_000_000, "1.0M"},
		{1_640_000, "1.6M"},
		{1_000_000_000, "1.00B"},
		{13_330_000_000, "13.33B"},
	}
	for _, c := range cases {
		if got := human(c.n); got != c.want {
			t.Errorf("human(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestHexDecode(t *testing.T) {
	out, err := hexDecode("deadbeef")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []byte{0xde, 0xad, 0xbe, 0xef}
	if len(out) != len(want) {
		t.Fatalf("len = %d, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("byte %d = %x, want %x", i, out[i], want[i])
		}
	}
}

func TestHexDecodeEmpty(t *testing.T) {
	out, err := hexDecode("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty output, got %x", out)
	}
}

func TestHexDecodeInvalid(t *testing.T) {
	if _, err := hexDecode("zz"); err == nil {
		t.Fatal("expected error decoding non-hex input")
	}
}

func TestNormalizeAcct(t *testing.T) {
	in := []byte{1, 2, 3}
	out := normalizeAcct(in)
	if len(out) != 3 || out[0] != 1 || out[1] != 2 || out[2] != 3 {
		t.Fatalf("normalizeAcct changed the value: got %v", out)
	}
}

func TestExactBlockOf(t *testing.T) {
	k := make([]byte, 10)
	// last 4 bytes big-endian = 0x00000042
	k[6], k[7], k[8], k[9] = 0x00, 0x00, 0x00, 0x42
	if got := exactBlockOf(k); got != 0x42 {
		t.Fatalf("exactBlockOf = %d, want %d", got, 0x42)
	}
}

func TestHB(t *testing.T) {
	if got := hb(0); got == "" {
		t.Fatal("hb(0) returned empty string")
	}
	if got := hb(1 << 30); got == "" {
		t.Fatal("hb(1GiB) returned empty string")
	}
}

func TestMaxHelper(t *testing.T) {
	if max(uint64(1), uint64(2)) != 2 {
		t.Fatal("max(1,2) != 2")
	}
	if max(uint64(5), uint64(2)) != 5 {
		t.Fatal("max(5,2) != 5")
	}
}
