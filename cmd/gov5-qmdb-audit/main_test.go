package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

func TestAuditRejectsRuntimeAndMalformedDigestsBeforeOpeningDatabase(t *testing.T) {
	for _, dir := range []string{
		"/data/blockchain/gov5-performance-5m/runtime/s192r1/e/chaindata",
		filepath.Join(t.TempDir(), "audit-copy", "chaindata"),
		"relative/audit-copy/chaindata",
	} {
		if _, _, err := copyDataFile(dir); err == nil {
			t.Fatalf("accepted non-validation copy %s", dir)
		}
	}
	if err := run(context.Background(), []string{"--copy-db", "/does-not-exist", "--genesis", "/genesis", "--output", "/output", "--expected-db-sha256", strings.Repeat("a", 64), "--expected-genesis-sha256", strings.Repeat("G", 64)}); err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("digest failed after opening a database: %v", err)
	}
}

func TestAuditRejectsMiskeyedAncestryHeader(t *testing.T) {
	db := auditTestDB(t)
	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), Root: types.Hash{31: 7}}
	valid := header.Hash()
	wrong := types.Hash{31: 8}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		encoded, err := header.Marshal()
		if err != nil {
			return err
		}
		if err := tx.Put(modules.Headers, modules.HeaderKey(1, valid), encoded); err != nil {
			return err
		}
		return tx.Put(modules.Headers, modules.HeaderKey(1, wrong), encoded)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		if _, err := checkedHeader(tx, valid, 1); err != nil {
			return err
		}
		if _, err := checkedHeader(tx, wrong, 1); err == nil {
			t.Fatal("accepted header bytes under another parent hash")
		}
		if _, err := checkedHeader(tx, valid, 2); err == nil {
			t.Fatal("accepted wrong header height")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
