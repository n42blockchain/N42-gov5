/*
   Additional coverage for checkCompatible branches not exercised in
   chain_config_g35_test.go (Petersburg/Constantinople interaction, later
   forks, numEqual, GetTargetBlobGasPerBlock, blobSchedule nil-receiver).
*/

package chain

import "testing"

func TestG35CheckCompatibleRemainingForks(t *testing.T) {
	cases := []struct {
		name string
		old  *Config
		new  *Config
		head uint64
		want bool // true = expect incompatible
	}{
		{"Byzantium", &Config{ByzantiumBlock: bi(1)}, &Config{ByzantiumBlock: bi(2)}, 5, true},
		{"Constantinople", &Config{ConstantinopleBlock: bi(1)}, &Config{ConstantinopleBlock: bi(2)}, 5, true},
		{"Istanbul", &Config{IstanbulBlock: bi(1)}, &Config{IstanbulBlock: bi(2)}, 5, true},
		{"MuirGlacier", &Config{MuirGlacierBlock: bi(1)}, &Config{MuirGlacierBlock: bi(2)}, 5, true},
		{"Berlin", &Config{BerlinBlock: bi(1)}, &Config{BerlinBlock: bi(2)}, 5, true},
		{"London", &Config{LondonBlock: bi(1)}, &Config{LondonBlock: bi(2)}, 5, true},
		{"ArrowGlacier", &Config{ArrowGlacierBlock: bi(1)}, &Config{ArrowGlacierBlock: bi(2)}, 5, true},
		{"GrayGlacier", &Config{GrayGlacierBlock: bi(1)}, &Config{GrayGlacierBlock: bi(2)}, 5, true},
		{"MergeNetsplit", &Config{MergeNetsplitBlock: bi(1)}, &Config{MergeNetsplitBlock: bi(2)}, 5, true},
		{"identical-compatible", &Config{LondonBlock: bi(1)}, &Config{LondonBlock: bi(1)}, 5, false},
	}
	for _, c := range cases {
		err := c.old.CheckCompatible(c.new, c.head)
		got := err != nil
		if got != c.want {
			t.Fatalf("%s: expected incompatible=%v, got err=%v", c.name, c.want, err)
		}
	}
}

func TestG35CheckCompatiblePetersburgEqualsConstantinople(t *testing.T) {
	// Petersburg == Constantinople on both sides: no error, satisfying the
	// "Petersburg may trail Constantinople" exception.
	old := &Config{ConstantinopleBlock: bi(5)}
	newer := &Config{ConstantinopleBlock: bi(5), PetersburgBlock: bi(5)}
	if err := old.CheckCompatible(newer, 10); err != nil {
		t.Fatalf("expected compatible Petersburg==Constantinople, got %v", err)
	}
}

func TestG35NumEqual(t *testing.T) {
	if !numEqual(nil, nil) {
		t.Fatalf("expected nil == nil")
	}
	if numEqual(nil, bi(1)) {
		t.Fatalf("expected nil != non-nil")
	}
	if numEqual(bi(1), nil) {
		t.Fatalf("expected non-nil != nil")
	}
	if !numEqual(bi(5), bi(5)) {
		t.Fatalf("expected equal values to compare equal")
	}
}

func TestG35GetTargetBlobGasPerBlock(t *testing.T) {
	cfg := &Config{CancunTime: bi(0)}
	if got := cfg.GetTargetBlobGasPerBlock(0); got == 0 {
		t.Fatalf("expected non-zero target blob gas per block")
	}
}

func TestG35BlobScheduleNilReceiver(t *testing.T) {
	var b *BlobSchedule
	if got := b.TargetBlobsPerBlock(false, false); got != 3 {
		t.Fatalf("expected Cancun default target from nil schedule, got %d", got)
	}
	if got := b.MaxBlobsPerBlock(true, false); got != 9 {
		t.Fatalf("expected Prague default max from nil schedule, got %d", got)
	}
	if got := b.BaseFeeUpdateFraction(true, true); got != 8346618 {
		t.Fatalf("expected Osaka default update fraction from nil schedule, got %d", got)
	}
}

func TestG35BorKeyValueConfigHelper(t *testing.T) {
	m := map[string]uint64{"0": 100, "10": 200, "20": 300}
	if got := borKeyValueConfigHelper(m, 5); got != 100 {
		t.Fatalf("expected 100 for number before first transition, got %d", got)
	}
	if got := borKeyValueConfigHelper(m, 15); got != 200 {
		t.Fatalf("expected 200 for number in second range, got %d", got)
	}
	if got := borKeyValueConfigHelper(m, 25); got != 300 {
		t.Fatalf("expected 300 for number past last transition, got %d", got)
	}
}

func TestG35BorKeyValueConfigHelperPanicsOnBadKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for non-numeric key")
		}
	}()
	m := map[string]uint64{"not-a-number": 1}
	borKeyValueConfigHelper(m, 0)
}
