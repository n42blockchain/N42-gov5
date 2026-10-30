// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// headerParityFixtures are the header shapes the Rust client's
// header-field-parity tests decode byte for byte (n42-rs
// crates/n42/h2-consensus/testdata/header_field_parity/): the trailing fields
// from Glamsterdam on, with the slot (the block number, as the native chain
// stamps it) at element 23 and MobileRegistryRoot at 24.
func headerParityFixtures() map[string]*Header {
	slot := uint64(1001) // balSampleHeader's number
	root := types.HexToHash("0x4242424242424242424242424242424242424242424242424242424242424242")
	bal := types.HexToHash("0x99aabbccddeeff00112233445566778899aabbccddeeff0011223344556677ff")

	// The native chain as configured today: balTime unset, so element 22 is
	// gov5's empty placeholder.
	slotRoot := balSampleHeader()
	slotRoot.SlotNumber = &slot
	slotRoot.MobileRegistryRoot = &root

	balSlotRoot := balSampleHeader()
	balSlotRoot.BlockAccessListHash = &bal
	balSlotRoot.SlotNumber = &slot
	balSlotRoot.MobileRegistryRoot = &root

	// Ethereum's Amsterdam shape: no root.
	amsterdam := balSampleHeader()
	amsterdam.BlockAccessListHash = &bal
	amsterdam.SlotNumber = &slot

	return map[string]*Header{
		"gov5_header_slot23_root24":       slotRoot,
		"gov5_header_bal22_slot23_root24": balSlotRoot,
		"gov5_header_bal22_slot23":        amsterdam,
	}
}

// TestWriteHeaderParityFixtures writes the fixtures (raw RLP, one file each,
// plus their gov5 hashes) when N42_HEADER_PARITY_FIXTURE_DIR names a
// directory; otherwise it only checks that every fixture round-trips.
func TestWriteHeaderParityFixtures(t *testing.T) {
	dir := os.Getenv("N42_HEADER_PARITY_FIXTURE_DIR")
	hashes := map[string]string{}
	for name, h := range headerParityFixtures() {
		enc, err := rlp.EncodeToBytes(h)
		if err != nil {
			t.Fatal(err)
		}
		var back Header
		if err := rlp.DecodeBytes(enc, &back); err != nil || back.Hash() != h.Hash() {
			t.Fatalf("%s: round trip err=%v hash %s want %s", name, err, back.Hash().Hex(), h.Hash().Hex())
		}
		hashes[name] = h.Hash().Hex()
		if dir != "" {
			if err := os.WriteFile(filepath.Join(dir, name+".rlp"), enc, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if dir == "" {
		return
	}
	out, err := json.MarshalIndent(hashes, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gov5_header_hashes.json"), append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
