package txlookup

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func testAnchorIdentity(t *testing.T, chainID int64) types.Hash {
	t.Helper()
	identity, err := CanonicalChainIdentity(big.NewInt(chainID), types.Hash{0xa1})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestCanonicalChainIdentityInputs(t *testing.T) {
	genesis := types.Hash{0xa1}
	a, err := CanonicalChainIdentity(big.NewInt(1), genesis)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalChainIdentity(big.NewInt(2), genesis)
	if err != nil || a == b {
		t.Fatalf("chain IDs not distinguished: %x, %x, %v", a, b, err)
	}
	c, err := CanonicalChainIdentity(big.NewInt(1), types.Hash{0xb2})
	if err != nil || a == c {
		t.Fatalf("genesis hashes not distinguished: %x, %x, %v", a, c, err)
	}
	for _, bad := range []*big.Int{nil, big.NewInt(-1)} {
		if _, err := CanonicalChainIdentity(bad, genesis); err == nil {
			t.Fatalf("accepted invalid chain ID %v", bad)
		}
	}
	if _, err := CanonicalChainIdentity(big.NewInt(1), types.Hash{}); err == nil {
		t.Fatal("accepted zero genesis")
	}
}

// A separate process must make the same admission decision using only the
// committed frame. No in-memory builder or owner state is carried across.
func TestAnchoredSegmentFreshProcessAdmission(t *testing.T) {
	if dir := os.Getenv("TXLOOKUP_ANCHOR_HELPER_DIR"); dir != "" {
		runAnchorAdmissionHelper(t, dir)
		return
	}
	identity := testAnchorIdentity(t, 1)
	endHash := types.Hash{2}
	build := func(anchored bool) string {
		dir := t.TempDir()
		src := func(n uint64) ([]types.Hash, error) { return []types.Hash{{byte(n), 1}, {byte(n), 2}}, nil }
		var seg *BuiltSegment
		var err error
		if anchored {
			seg, err = BuildSegmentFileAnchored(context.Background(), dir, 1, 3, src, SegmentAnchor{Identity: identity, EndCanonicalHash: endHash})
		} else {
			seg, err = BuildSegmentFile(context.Background(), dir, 1, 3, src)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CommitBuiltSegments(dir, []*BuiltSegment{seg}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	check := func(dir, mode string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestAnchoredSegmentFreshProcessAdmission$")
		cmd.Env = append(os.Environ(), "TXLOOKUP_ANCHOR_HELPER_DIR="+dir, "TXLOOKUP_ANCHOR_HELPER_MODE="+mode)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s helper: %v\n%s", mode, err, out)
		}
		if !strings.Contains(string(out), "ANCHOR_ADMISSION_OK") {
			t.Fatalf("%s helper lacked admission receipt: %s", mode, out)
		}
	}
	dir := build(true)
	check(dir, "valid")
	check(dir, "foreign")
	check(dir, "foreign_genesis")
	check(dir, "replaced")
	check(dir, "below_head")
	emptyDir := t.TempDir()
	empty, err := BuildSegmentFileAnchored(context.Background(), emptyDir, 1, 3,
		func(uint64) ([]types.Hash, error) { return nil, nil }, SegmentAnchor{Identity: identity, EndCanonicalHash: endHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CommitBuiltSegments(emptyDir, []*BuiltSegment{empty}); err != nil {
		t.Fatal(err)
	}
	check(emptyDir, "empty")

	legacy := build(false)
	check(legacy, "legacy")
	mixed := build(true)
	legacyTail, err := BuildSegmentFile(context.Background(), mixed, 3, 4,
		func(uint64) ([]types.Hash, error) { return []types.Hash{{3, 1}}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CommitBuiltSegments(mixed, []*BuiltSegment{legacyTail}); err != nil {
		t.Fatal(err)
	}
	check(mixed, "mixed")

	// Change bytes without changing frame sizes. Both corruption modes must be
	// caught by the EFD3 digest before parsing RI or accepting Coverage.
	for _, region := range []string{"ef", "ri", "unknown", "truncated"} {
		dir := build(true)
		path := filepath.Join(dir, "txindex.0000.cdat")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		datLen := binary.LittleEndian.Uint32(data[:4])
		switch region {
		case "ef":
			data[4+datV3HeaderSize+20] ^= 1
		case "ri":
			data[4+int(datLen)+4+20] ^= 1
		case "unknown":
			copy(data[4:8], "EFD4")
		case "truncated":
			data = data[:4+48]
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		check(dir, region)
	}
}

func runAnchorAdmissionHelper(t *testing.T, dir string) {
	mode := os.Getenv("TXLOOKUP_ANCHOR_HELPER_MODE")
	identity := testAnchorIdentity(t, 1)
	endHash := types.Hash{2}
	if mode == "foreign" {
		identity = testAnchorIdentity(t, 2)
	}
	if mode == "foreign_genesis" {
		var err error
		identity, err = CanonicalChainIdentity(big.NewInt(1), types.Hash{0xb2})
		if err != nil {
			t.Fatal(err)
		}
	}
	if mode == "replaced" {
		endHash = types.Hash{8}
	}
	head := uint64(2)
	if mode == "below_head" {
		head = 1
	}
	svc, err := NewAnchoredService(dir, identity, func(n uint64) (types.Hash, error) {
		if n != 2 {
			return types.Hash{}, fmt.Errorf("unexpected canonical height %d", n)
		}
		return endHash, nil
	}, head)
	if mode == "valid" || mode == "empty" {
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		if start, end := svc.Coverage(); start != 1 || end != 3 {
			t.Fatalf("coverage %d-%d", start, end)
		}
		if mode == "valid" {
			svc.SetPositionVerifier(func(blockNum, txIndex uint64, h types.Hash) (bool, error) {
				if blockNum != uint64(h[0]) || txIndex != uint64(h[1]-1) {
					t.Fatalf("wrong candidate position %d/%d for %x", blockNum, txIndex, h)
				}
				return true, nil
			})
		}
		for n := uint64(1); n < 3 && mode == "valid"; n++ {
			for ordinal := byte(1); ordinal <= 2; ordinal++ {
				h := types.Hash{byte(n), ordinal}
				found, err := svc.Lookup(nil, h)
				if err != nil || found == nil || *found != n {
					t.Fatalf("lookup %x: %v, %v", h, found, err)
				}
			}
		}
		if mode == "empty" {
			if found, err := svc.Lookup(nil, types.Hash{1, 2}); err != nil || found != nil {
				t.Fatalf("empty lookup = %v, %v", found, err)
			}
		}
		if mode == "valid" {
			svc.SetPositionVerifier(func(uint64, uint64, types.Hash) (bool, error) { return false, fmt.Errorf("canonical body I/O failure") })
			if _, err := svc.Lookup(nil, types.Hash{1, 2}); err == nil || !strings.Contains(err.Error(), "canonical body I/O failure") {
				t.Fatalf("body I/O failure hidden as miss: %v", err)
			}
			svc.SetPositionVerifier(func(uint64, uint64, types.Hash) (bool, error) { return false, nil })
			if found, err := svc.Lookup(nil, types.Hash{1, 2}); err != nil || found != nil {
				t.Fatalf("phantom rejection = %v, %v", found, err)
			}
		}
	} else if err == nil {
		svc.Close()
		t.Fatalf("%s unexpectedly admitted", mode)
	}
	if mode == "legacy" {
		ordinary, err := NewService(dir)
		if err != nil {
			t.Fatalf("ordinary V2 reader: %v", err)
		}
		defer ordinary.Close()
		if found, err := ordinary.Lookup(nil, types.Hash{1, 2}); err != nil || found == nil || *found != 1 {
			t.Fatalf("ordinary V2 lookup = %v, %v", found, err)
		}
	}
	fmt.Println("ANCHOR_ADMISSION_OK", mode)
}
