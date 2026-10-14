// n42-reth-state-dump exports a gov5 QMDB state snapshot in reth init-state
// JSONL format and writes the exact applied-head header as canonical RLP.
//
// The source node must be stopped (or the MDBX directory must be a filesystem
// snapshot). A single read transaction binds the header, accounts, storage and
// bytecode to one applied QMDB head.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

type dumpAccount struct {
	Address string            `json:"address"`
	Balance string            `json:"balance"`
	Nonce   string            `json:"nonce"`
	Code    string            `json:"code,omitempty"`
	Storage map[string]string `json:"storage,omitempty"`
}

type manifest struct {
	Version       uint64 `json:"version"`
	Format        string `json:"format"`
	ChainID       uint64 `json:"chain_id"`
	BlockNumber   uint64 `json:"block_number"`
	BlockHash     string `json:"block_hash"`
	StateRoot     string `json:"state_root"`
	HeaderRLP     string `json:"header_rlp"`
	StateJSONL    string `json:"state_jsonl"`
	StateSHA256   string `json:"state_sha256"`
	Accounts      uint64 `json:"accounts"`
	StorageSlots  uint64 `json:"storage_slots"`
	CompleteState bool   `json:"complete_state"`
}

func main() {
	dbPath := flag.String("datadir", "", "stopped gov5 chaindata MDBX directory")
	outPath := flag.String("out", "", "reth init-state JSONL output")
	headerPath := flag.String("header-out", "", "raw RLP anchor header output (default <out>.header.rlp)")
	manifestPath := flag.String("manifest-out", "", "snapshot manifest output (default <out>.manifest.json)")
	chainID := flag.Uint64("chain-id", 94, "chain ID recorded in the manifest")
	limit := flag.Uint64("limit", 0, "test-only account limit; output is not a complete state when non-zero")
	flag.Parse()

	if *dbPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-reth-state-dump --datadir <chaindata> --out <state.jsonl>")
		os.Exit(2)
	}
	if *headerPath == "" {
		*headerPath = *outPath + ".header.rlp"
	}
	if *manifestPath == "" {
		*manifestPath = *outPath + ".manifest.json"
	}

	if err := run(*dbPath, *outPath, *headerPath, *manifestPath, *chainID, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "n42-reth-state-dump:", err)
		os.Exit(1)
	}
}

func run(dbPath, outPath, headerPath, manifestPath string, chainID, limit uint64) error {
	if err := validateExportPaths(dbPath, outPath, headerPath, manifestPath); err != nil {
		return err
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		Accede().
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open source (stop the gov5 node before exporting): %w", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return fmt.Errorf("begin read snapshot: %w", err)
	}
	defer tx.Rollback()

	blockNumber, appliedHash, err := exportHead(tx)
	if err != nil {
		return err
	}

	header := rawdb.ReadHeader(tx, types.Hash(appliedHash), blockNumber)
	if header == nil {
		return fmt.Errorf("applied header #%d %x is absent", blockNumber, appliedHash)
	}
	if got := header.Hash(); got != types.Hash(appliedHash) {
		return fmt.Errorf("applied header hash mismatch: marker=%x header=%x", appliedHash, got)
	}
	if header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != blockNumber {
		return fmt.Errorf("applied header number does not match export height %d", blockNumber)
	}
	genesis, err := rawdb.ReadCanonicalHash(tx, 0)
	if err != nil {
		return err
	}
	cfg, err := rawdb.ReadChainConfig(tx, genesis)
	if err != nil {
		return err
	}
	if cfg == nil || cfg.ChainID == nil || !cfg.ChainID.IsUint64() || cfg.ChainID.Uint64() != chainID {
		return fmt.Errorf("manifest chain ID %d does not match source chain config", chainID)
	}
	// Replay v2 generations may predate QMDBApplied. Authenticate the fallback
	// against the actual forest in the same read transaction before publishing files.
	computer := commitment.NewQMDBRootComputer()
	if err := computer.LoadFrom(tx); err != nil {
		return fmt.Errorf("load QMDB forest: %w", err)
	}
	if err := verifyExportRoot(computer.Tree().Root(), header.Root); err != nil {
		return err
	}
	if err := computer.Tree().VerifyPersistedValues(tx); err != nil {
		return fmt.Errorf("authenticate QMDB live entries: %w", err)
	}
	headerRLP, err := rlp.EncodeToBytes(header)
	if err != nil {
		return fmt.Errorf("encode anchor header RLP: %w", err)
	}

	if err := mkdirParents(outPath, headerPath, manifestPath); err != nil {
		return err
	}

	started := time.Now()
	out, err := os.CreateTemp(filepath.Dir(outPath), "."+filepath.Base(outPath)+".partial-*")
	if err != nil {
		return err
	}
	partial := out.Name()
	if err := out.Chmod(0o644); err != nil {
		_ = out.Close()
		_ = os.Remove(partial)
		return err
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(partial)
	}()

	digest := sha256.New()
	buffered := bufio.NewWriterSize(io.MultiWriter(out, digest), 4<<20)
	encoder := json.NewEncoder(buffered)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]string{"root": header.Root.Hex()}); err != nil {
		return err
	}

	accounts, slots, err := exportAccounts(tx, computer.Tree(), encoder, limit, logger, started)
	if err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := writeAtomic(headerPath, headerRLP, 0o644); err != nil {
		return fmt.Errorf("write anchor header: %w", err)
	}
	if err := os.Rename(partial, outPath); err != nil {
		return err
	}

	m := manifest{
		Version:       1,
		Format:        "reth-init-state-jsonl+gov5-qmdb-anchor",
		ChainID:       chainID,
		BlockNumber:   blockNumber,
		BlockHash:     types.Hash(appliedHash).Hex(),
		StateRoot:     header.Root.Hex(),
		HeaderRLP:     filepath.Base(headerPath),
		StateJSONL:    filepath.Base(outPath),
		StateSHA256:   hex.EncodeToString(digest.Sum(nil)),
		Accounts:      accounts,
		StorageSlots:  slots,
		CompleteState: limit == 0,
	}
	manifestBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writeAtomic(manifestPath, manifestBytes, 0o644); err != nil {
		return err
	}

	logger.Info("reth state snapshot exported",
		"block", blockNumber,
		"hash", types.Hash(appliedHash),
		"root", header.Root,
		"accounts", accounts,
		"storageSlots", slots,
		"elapsed", time.Since(started).Round(time.Second),
		"sha256", m.StateSHA256)
	return nil
}

func exportAccounts(tx kv.Tx, tree *qmdb.Tree, encoder *json.Encoder, limit uint64, logger log.Logger, started time.Time) (uint64, uint64, error) {
	accountCursor, err := tx.Cursor(modules.Account)
	if err != nil {
		return 0, 0, fmt.Errorf("open Account cursor: %w", err)
	}
	defer accountCursor.Close()
	storageCursor, err := tx.Cursor(modules.Storage)
	if err != nil {
		return 0, 0, fmt.Errorf("open Storage cursor: %w", err)
	}
	defer storageCursor.Close()

	storageKey, storageValue, storageErr := storageCursor.First()
	var accounts, slots uint64
	var exportedEmptyAccounts uint64
	for address, encoded, err := accountCursor.First(); ; address, encoded, err = accountCursor.Next() {
		if err != nil {
			return accounts, slots, fmt.Errorf("iterate Account: %w", err)
		}
		if address == nil {
			break
		}
		if limit > 0 && accounts >= limit {
			break
		}
		if len(address) != types.AddressLength {
			return accounts, slots, fmt.Errorf("malformed Account key length %d", len(address))
		}
		var stateAccount account.StateAccount
		if err := stateAccount.DecodeForStorage(encoded); err != nil {
			return accounts, slots, fmt.Errorf("decode account %x: %w", address, err)
		}
		addr := types.BytesToAddress(address)
		accountValue := commitment.EncodeAccountValue(&stateAccount)
		if err := verifyExportValue(tree, qmdb.Hash(commitment.AccountKeyHash(addr)), accountValue); err != nil {
			return accounts, slots, fmt.Errorf("authenticate account %x: %w", address, err)
		}
		if bytes.Equal(accountValue, []byte{0}) {
			exportedEmptyAccounts++
		}

		if storageErr != nil {
			return accounts, slots, fmt.Errorf("iterate Storage: %w", storageErr)
		}
		if storageKey != nil && len(storageKey) < types.AddressLength {
			return accounts, slots, fmt.Errorf("malformed Storage key length %d", len(storageKey))
		}
		for storageKey != nil && bytes.Compare(storageKey[:types.AddressLength], address) < 0 {
			return accounts, slots, fmt.Errorf("orphan Storage row %x has no Account row", storageKey)
		}
		storage := make(map[string]string)
		for storageKey != nil && bytes.Equal(storageKey[:types.AddressLength], address) {
			if len(storageKey) != types.AddressLength+types.HashLength || len(storageValue) > types.HashLength {
				return accounts, slots, fmt.Errorf("malformed Storage row key=%d value=%d", len(storageKey), len(storageValue))
			}
			var value [types.HashLength]byte
			copy(value[len(value)-len(storageValue):], storageValue)
			key := types.BytesToHash(storageKey[types.AddressLength:])
			if err := verifyExportValue(tree, qmdb.Hash(commitment.StorageKeyHash(addr, key)), value[:]); err != nil {
				return accounts, slots, fmt.Errorf("authenticate storage %x: %w", storageKey, err)
			}
			storage["0x"+hex.EncodeToString(storageKey[types.AddressLength:])] = "0x" + hex.EncodeToString(value[:])
			slots++
			storageKey, storageValue, storageErr = storageCursor.Next()
			if storageErr != nil {
				return accounts, slots, fmt.Errorf("iterate Storage: %w", storageErr)
			}
			if storageKey != nil && len(storageKey) < types.AddressLength {
				return accounts, slots, fmt.Errorf("malformed Storage key length %d", len(storageKey))
			}
		}

		item := dumpAccount{
			Address: "0x" + hex.EncodeToString(address),
			Balance: quantity(stateAccount.Balance.Bytes()),
			Nonce:   fmt.Sprintf("0x%x", stateAccount.Nonce),
		}
		if len(storage) != 0 {
			item.Storage = storage
		}
		if !stateAccount.IsEmptyCodeHash() {
			code, err := tx.GetOne(modules.Code, stateAccount.CodeHash[:])
			if err != nil {
				return accounts, slots, fmt.Errorf("read code for %x: %w", address, err)
			}
			if len(code) == 0 {
				return accounts, slots, fmt.Errorf("missing bytecode %x for account %x", stateAccount.CodeHash, address)
			}
			if crypto.Keccak256Hash(code) != stateAccount.CodeHash {
				return accounts, slots, fmt.Errorf("bytecode hash mismatch for account %x", address)
			}
			item.Code = "0x" + hex.EncodeToString(code)
		}
		if err := encoder.Encode(&item); err != nil {
			return accounts, slots, fmt.Errorf("encode account %x: %w", address, err)
		}
		accounts++
		if accounts%100_000 == 0 {
			logger.Info("exporting reth state JSONL", "accounts", accounts, "storageSlots", slots, "elapsed", time.Since(started).Round(time.Second))
		}
	}
	if limit == 0 && storageKey != nil {
		return accounts, slots, fmt.Errorf("orphan Storage rows remain at %x", storageKey)
	}
	if storageErr != nil {
		return accounts, slots, fmt.Errorf("iterate Storage: %w", storageErr)
	}
	if limit == 0 {
		if err := verifyExportCoverage(tx, tree, accounts+slots, exportedEmptyAccounts); err != nil {
			return accounts, slots, err
		}
	}
	return accounts, slots, nil
}

func verifyExportCoverage(tx kv.Tx, tree *qmdb.Tree, exported, exportedEmpty uint64) error {
	// QMDB retains EIP-161 empty accounts that the plain table removes. Their
	// canonical account value is one zero byte; storage values are 32 bytes.
	// All other live keys must appear exactly once among the authenticated rows.
	var retainedEmpty uint64
	err := tx.ForEach(qmdb.EntryTable, nil, func(k, v []byte) error {
		if len(k) != 8 {
			return fmt.Errorf("malformed QMDB entry key length %d", len(k))
		}
		active, ok := tree.SlotActive(binary.BigEndian.Uint64(k))
		if ok && active && len(v) == 33 && v[32] == 0 {
			retainedEmpty++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("check export coverage: %w", err)
	}
	live := uint64(tree.LiveCount())
	if retainedEmpty < exportedEmpty || retainedEmpty > live || exported != live-retainedEmpty+exportedEmpty {
		return fmt.Errorf("plain state is incomplete: exported %d rows for %d QMDB live keys (%d retained empty accounts)", exported, live, retainedEmpty)
	}
	return nil
}

// A matching forest root alone does not authenticate plain-table data. Bind
// every exported value to a live leaf, including its persisted entry bytes.
func verifyExportValue(tree *qmdb.Tree, key qmdb.Hash, value []byte) error {
	committed, ok, err := tree.GetChecked(key)
	if err != nil {
		return err
	}
	if !ok || !bytes.Equal(committed, value) {
		return fmt.Errorf("value is not authenticated by the QMDB root")
	}
	return nil
}

func quantity(value []byte) string {
	digits := strings.TrimLeft(hex.EncodeToString(value), "0")
	if digits == "" {
		return "0x0"
	}
	return "0x" + digits
}

func mkdirParents(paths ...string) error {
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".partial-*")
	if err != nil {
		return err
	}
	partial := f.Name()
	defer os.Remove(partial)
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(partial, path)
}

// exportHead prefers the explicit applied marker. Older replay v2 databases
// lack it; their canonical head is usable only with the forest root check above.
func exportHead(tx kv.Getter) (uint64, [32]byte, error) {
	number, hash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return 0, [32]byte{}, fmt.Errorf("read QMDB applied head: %w", err)
	}
	if !ok {
		hash = rawdb.ReadHeadBlockHash(tx)
		n := rawdb.ReadHeaderNumber(tx, types.Hash(hash))
		if hash == ([32]byte{}) || n == nil {
			return 0, [32]byte{}, fmt.Errorf("database has neither a QMDB applied marker nor a readable head")
		}
		number = *n
	}
	canonical, err := rawdb.ReadCanonicalHash(tx, number)
	if err != nil {
		return 0, [32]byte{}, err
	}
	if canonical != types.Hash(hash) {
		return 0, [32]byte{}, fmt.Errorf("export head %d/%x is not canonical", number, hash)
	}
	return number, hash, nil
}

func verifyExportRoot(root qmdb.Hash, expected types.Hash) error {
	if root != qmdb.Hash(expected) {
		return fmt.Errorf("QMDB root %x does not match export header root %x", root, expected)
	}
	return nil
}
