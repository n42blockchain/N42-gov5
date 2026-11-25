// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshotjoin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// Result is what Import installed.
type Result struct {
	Pivot        uint64
	PivotHash    types.Hash
	Root         types.Hash
	Accounts     uint64
	Slots        uint64
	LiveLeaves   uint64
	AnchorView   uint64
	AnchorNumber uint64
	Headers      int
}

// stateTables are replaced wholesale by an import: the plain state, the
// bytecode and every QMDB table the genesis seeding wrote.
var stateTables = []string{
	modules.Account, modules.Storage, modules.Code,
	modules.QMDBEntries, modules.QMDBTwigs, modules.QMDBMeta, modules.QMDBIndex, modules.QMDBTwigLeaves,
	modules.QMDBDeathStamps, modules.QMDBBlockPos, modules.QMDBKeyVers, modules.QMDBTopBand, modules.QMDBUndoWindow,
	modules.ExecutedResult,
}

// Import installs the bundle in dir into tx, a fresh profile-1 database that
// holds only the genesis of (cfg, genesisHash). Every check runs before the
// caller commits; any error leaves tx to be rolled back:
//
//   - the anchor's QC verifies in the H2-v4 commit domain against the
//     genesis validator set, its headers are a parent-hash chain from the
//     QC's block down through P+1 and P, and a chain starting at block 1
//     extends this genesis;
//   - the header files are the anchor's headers P and P+1, and block P's
//     body matches its header;
//   - the portable snapshot is bound to this chain and genesis, labelled
//     (P, hash(P)), and rebuilds to header(P+1).Root (k = 1);
//   - the JSONL's root line is header(P+1).Root, every row is a live leaf
//     with the same bytes, rows ascend by address, and every live leaf is a
//     row except the EIP-161 empty accounts QMDB keeps (n42-rs's rule).
//
// It then writes the plain state and code from the JSONL, the QMDB forest,
// headers max(P-255,1)..P as canonical, block P as the head (head, header,
// committed and applied markers), and P's executed result taken from
// header(P+1) so the deferred check of P+1 passes. Blocks above P come by
// range sync.
func Import(tx kv.RwTx, cfg *params.ChainConfig, genesisHash types.Hash, dir string, logger log.Logger) (*Result, error) {
	if logger == nil {
		logger = log.New()
	}
	started := time.Now()
	b, err := ReadBundle(dir)
	if err != nil {
		return nil, err
	}
	id, err := IdentityFor(cfg, genesisHash)
	if err != nil {
		return nil, err
	}
	if cfg.StateScheme != string(params.StateCommitmentPresetQMDB) {
		return nil, fmt.Errorf("the chain commits with %q, the bundle carries a QMDB forest", cfg.StateScheme)
	}
	m := b.Manifest
	if m.ChainID != id.ChainID || !strings.EqualFold(m.GenesisHash, genesisHash.Hex()) {
		return nil, fmt.Errorf("wrong chain: the bundle is for chain %d genesis %s, this datadir is chain %d genesis %s", m.ChainID, m.GenesisHash, id.ChainID, genesisHash.Hex())
	}

	// Headers and the anchor.
	pivotHdr, err := decodeHeader(b.Header, FileHeader)
	if err != nil {
		return nil, err
	}
	nextHdr, err := decodeHeader(b.NextHeader, FileNextHeader)
	if err != nil {
		return nil, err
	}
	pivot, pivotHash := pivotHdr.Number.Uint64(), pivotHdr.Hash()
	if pivot == 0 {
		return nil, errors.New("the pivot is the genesis block; nothing to import")
	}
	if m.PivotNumber != pivot || !strings.EqualFold(m.PivotHash, pivotHash.Hex()) {
		return nil, fmt.Errorf("manifest pivot %d %s is not %s's block %d %s", m.PivotNumber, m.PivotHash, FileHeader, pivot, pivotHash.Hex())
	}
	anchor, err := DecodeAnchor(b.Anchor)
	if err != nil {
		return nil, err
	}
	aPivot, aPost, err := id.VerifyAnchor(anchor, pivot, 1)
	if err != nil {
		return nil, err
	}
	if aPivot.Hash() != pivotHash {
		return nil, fmt.Errorf("%s is block %d %x, the certified chain has %x there", FileHeader, pivot, pivotHash, aPivot.Hash())
	}
	if nextHdr.Hash() != aPost.Hash() {
		return nil, fmt.Errorf("%s is %x, the certified chain has block %d %x", FileNextHeader, nextHdr.Hash(), pivot+1, aPost.Hash())
	}
	if !cfg.IsDeferredExecution(nextHdr.Time) {
		return nil, fmt.Errorf("block %d is not under deferred execution; the k = 1 label does not apply", pivot+1)
	}
	root := nextHdr.Root
	for _, h := range anchor.Headers {
		if h.Difficulty != nil && !h.Difficulty.IsZero() {
			return nil, fmt.Errorf("block %d has difficulty %s; profile 1 requires 0", h.Number.Uint64(), h.Difficulty)
		}
	}

	// Block P.
	blk := new(block.Block)
	if err := rlp.DecodeBytes(b.Block, blk); err != nil {
		return nil, fmt.Errorf("%s: %w", FileBlock, err)
	}
	if blk.Hash() != pivotHash {
		return nil, fmt.Errorf("%s is block %x, the pivot is %x", FileBlock, blk.Hash(), pivotHash)
	}
	if got := hash.DeriveShaErigon(transaction.EthTransactions(blk.Transactions())); got != pivotHdr.TxHash {
		return nil, fmt.Errorf("%s: transactions root %x, header %x", FileBlock, got, pivotHdr.TxHash)
	}
	if body, ok := blk.Body().(*block.Body); ok && pivotHdr.WithdrawalsHash != nil {
		if got := hash.DeriveSha(block.Rewards(body.Rewards)); got != *pivotHdr.WithdrawalsHash {
			return nil, fmt.Errorf("%s: rewards root %x, header withdrawalsRoot %x", FileBlock, got, *pivotHdr.WithdrawalsHash)
		}
	}

	// The forest.
	tree, verification, err := qmdb.ImportPortableSnapshot(b.QMDB, id.ChainID, qmdb.Hash(genesisHash))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileQMDB, err)
	}
	md := verification.Metadata
	if md.BlockNumber != pivot || md.BlockHash != qmdb.Hash(pivotHash) {
		return nil, fmt.Errorf("%s is labelled block %d %x, the pivot is %d %x", FileQMDB, md.BlockNumber, md.BlockHash, pivot, pivotHash)
	}
	if tree.Root() != qmdb.Hash(root) {
		return nil, fmt.Errorf("%s rebuilds to root %x, header(%d).Root is %x (state after block %d)", FileQMDB, tree.Root(), pivot+1, root, pivot)
	}

	// Replace the genesis state.
	for _, table := range stateTables {
		if err := tx.ClearBucket(table); err != nil {
			return nil, fmt.Errorf("clear %s: %w", table, err)
		}
	}

	// The JSONL: checked row by row against the forest and written.
	accounts, slots, err := importState(tx, tree, root, dir, m)
	if err != nil {
		return nil, err
	}

	// The forest, persisted as the node reloads it.
	tree.SetLeafStore(qmdb.LeafStoreFromGetter(tx))
	if _, _, err := tree.FlushTo(tx, 0); err != nil {
		return nil, fmt.Errorf("persist the QMDB forest: %w", err)
	}
	tree.CommitFlush()
	reload := commitment.NewQMDBRootComputer()
	reload.SetCold(tx)
	if err := reload.LoadFrom(tx); err != nil {
		return nil, fmt.Errorf("reload the persisted QMDB forest: %w", err)
	}
	if got := reload.Root(); got != root {
		return nil, fmt.Errorf("the persisted QMDB forest reloads to %x, want %x", got, root)
	}
	if err := reload.Tree().VerifyPersistedValues(tx); err != nil {
		return nil, fmt.Errorf("the persisted QMDB forest: %w", err)
	}
	reload.SetCold(nil)

	// The chain: headers low..P canonical, block P the head.
	zero := uint256.NewInt(0)
	written := 0
	for _, h := range anchor.Headers {
		n := h.Number.Uint64()
		if n > pivot {
			continue
		}
		rawdb.WriteHeader(tx, h)
		if err := rawdb.WriteCanonicalHash(tx, h.Hash(), n); err != nil {
			return nil, err
		}
		if err := rawdb.WriteTd(tx, h.Hash(), n, zero); err != nil {
			return nil, err
		}
		written++
	}
	if err := rawdb.WriteBlock(tx, blk); err != nil {
		return nil, fmt.Errorf("write block %d: %w", pivot, err)
	}
	rawdb.WriteHeadBlockHash(tx, pivotHash)
	if err := rawdb.WriteHeadHeaderHash(tx, pivotHash); err != nil {
		return nil, err
	}
	if err := rawdb.WriteHotStuffCommittedHead(tx, pivotHash); err != nil {
		return nil, err
	}
	if err := rawdb.WriteQMDBApplied(tx, pivot, pivotHash); err != nil {
		return nil, err
	}
	// SYNC §5.3.1: the executed result of P is what header(P+1) carries.
	rh := rawdb.EmptyRequestsHash
	if nextHdr.RequestsHash != nil {
		rh = *nextHdr.RequestsHash
	}
	if err := rawdb.WriteExecutedResult(tx, pivotHash, rawdb.ExecutedResult{
		Root: nextHdr.Root, ReceiptHash: nextHdr.ReceiptHash, Bloom: nextHdr.Bloom, GasUsed: nextHdr.GasUsed, RequestsHash: rh,
	}); err != nil {
		return nil, err
	}
	res := &Result{
		Pivot: pivot, PivotHash: pivotHash, Root: root, Accounts: accounts, Slots: slots,
		LiveLeaves: uint64(tree.LiveCount()), AnchorView: anchor.QC.View, AnchorNumber: anchor.Headers[0].Number.Uint64(), Headers: written,
	}
	logger.Info("snapshot bundle verified and installed", "pivot", pivot, "hash", pivotHash, "postStateRoot", root,
		"accounts", accounts, "storageSlots", slots, "liveLeaves", res.LiveLeaves, "anchorView", res.AnchorView,
		"anchorBlock", res.AnchorNumber, "headers", written, "elapsed", time.Since(started).Round(time.Millisecond))
	return res, nil
}

func decodeHeader(data []byte, name string) (*block.Header, error) {
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "0x") {
		raw, err := hexutil.Decode(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		data = raw
	}
	h := new(block.Header)
	if err := rlp.DecodeBytes(data, h); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if h.Number == nil || !h.Number.IsUint64() {
		return nil, fmt.Errorf("%s: header without a number", name)
	}
	return h, nil
}

// jsonRow is one JSONL account line as reth's init-state reads it.
type jsonRow struct {
	Address types.Address     `json:"address"`
	Balance json.RawMessage   `json:"balance"`
	Nonce   json.RawMessage   `json:"nonce"`
	Code    hexutil.Bytes     `json:"code"`
	Storage map[string]string `json:"storage"`
}

// importState streams the JSONL: every row authenticated by tree and written
// as plain state, then the coverage rule. It checks the file against the
// manifest digest at the end.
func importState(tx kv.RwTx, tree *qmdb.Tree, root types.Hash, dir string, m *Manifest) (uint64, uint64, error) {
	f, err := os.Open(filepath.Join(dir, FileState))
	if err != nil {
		return 0, 0, fmt.Errorf("bundle file: %w", err)
	}
	defer f.Close()
	digest := newSHA256Reader(f)
	scanner := bufio.NewScanner(digest)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return 0, 0, fmt.Errorf("%s: %w", FileState, err)
		}
		return 0, 0, fmt.Errorf("%s is empty", FileState)
	}
	var first struct {
		Root *types.Hash `json:"root"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &first); err != nil || first.Root == nil {
		return 0, 0, fmt.Errorf("%s: the first line must be {\"root\": ...}", FileState)
	}
	if *first.Root != root {
		return 0, 0, fmt.Errorf("%s root line %x is not header(P+1).Root %x", FileState, *first.Root, root)
	}
	var (
		accounts, slots, matched, emptyRows uint64
		last                                *types.Address
		line                                = 1
	)
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var row jsonRow
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&row); err != nil {
			return 0, 0, fmt.Errorf("%s line %d: %w", FileState, line, err)
		}
		if last != nil && bytes.Compare(last[:], row.Address[:]) >= 0 {
			return 0, 0, fmt.Errorf("%s line %d: address %s is not above %s (rows must ascend strictly)", FileState, line, row.Address, *last)
		}
		addr := row.Address
		last = &addr
		balance, err := parseQuantity(row.Balance)
		if err != nil {
			return 0, 0, fmt.Errorf("%s line %d: balance: %w", FileState, line, err)
		}
		nonce, err := parseQuantity(row.Nonce)
		if err != nil || !nonce.IsUint64() {
			return 0, 0, fmt.Errorf("%s line %d: nonce: %v", FileState, line, err)
		}
		acct := account.NewAccount()
		acct.Nonce = nonce.Uint64()
		acct.Balance = *balance
		if len(row.Code) > 0 {
			acct.CodeHash = crypto.Keccak256Hash(row.Code)
		}
		leaf := commitment.EncodeAccountValue(&acct)
		if err := VerifyExportValue(tree, qmdb.Hash(commitment.AccountKeyHash(addr)), leaf); err != nil {
			return 0, 0, fmt.Errorf("%s line %d: account %s is not authenticated by the QMDB root: it is not a live leaf with these bytes", FileState, line, addr)
		}
		matched++
		if bytes.Equal(leaf, emptyAccountLeaf) {
			emptyRows++
		}
		if err := tx.Put(modules.Account, addr[:], leaf); err != nil {
			return 0, 0, err
		}
		if len(row.Code) > 0 {
			if err := tx.Put(modules.Code, acct.CodeHash[:], row.Code); err != nil {
				return 0, 0, err
			}
		}
		for k, v := range row.Storage {
			slot, err := parseHash32(k)
			if err != nil {
				return 0, 0, fmt.Errorf("%s line %d: storage key %q: %w", FileState, line, k, err)
			}
			value, err := parseHash32(v)
			if err != nil {
				return 0, 0, fmt.Errorf("%s line %d: storage value %q: %w", FileState, line, v, err)
			}
			key := qmdb.Hash(commitment.StorageKeyHash(addr, slot))
			if value == (types.Hash{}) {
				// A zero slot is no leaf (D5); reth's dump may still list it.
				if _, ok, err := tree.GetChecked(key); err != nil || ok {
					return 0, 0, fmt.Errorf("%s line %d: storage %s %s is zero in the dump but live in the QMDB tree", FileState, line, addr, slot)
				}
				continue
			}
			if err := VerifyExportValue(tree, key, value[:]); err != nil {
				return 0, 0, fmt.Errorf("%s line %d: storage %s %s is not authenticated by the QMDB root", FileState, line, addr, slot)
			}
			matched++
			slots++
			trimmed := new(uint256.Int).SetBytes(value[:]).Bytes()
			if err := tx.Put(modules.Storage, modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot.Bytes()), trimmed); err != nil {
				return 0, 0, err
			}
		}
		accounts++
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("%s: %w", FileState, err)
	}
	if want, ok := m.Files[FileState]; !ok || digest.hex() != want {
		return 0, 0, fmt.Errorf("bundle file %s: sha256 %s, manifest %s (damaged copy)", FileState, digest.hex(), want)
	}
	var retainedEmpty uint64
	tree.IndexRange(func(keyHash qmdb.Hash, _ uint64) bool {
		if v, ok := tree.Get(keyHash); ok && bytes.Equal(v, emptyAccountLeaf) {
			retainedEmpty++
		}
		return true
	})
	if err := checkCoverage(uint64(tree.LiveCount()), retainedEmpty, matched, emptyRows); err != nil {
		return 0, 0, fmt.Errorf("%s does not cover the QMDB tree: %w", FileState, err)
	}
	return accounts, slots, nil
}

// parseQuantity reads a JSON quantity: a "0x" hex string, a decimal string
// or a JSON number.
func parseQuantity(raw json.RawMessage) (*uint256.Int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return uint256.NewInt(0), nil
	}
	text := string(raw)
	if s, err := strconv.Unquote(text); err == nil {
		text = s
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		return uint256.FromHex(normalizeHex(text))
	}
	return uint256.FromDecimal(text)
}

// normalizeHex strips leading zeros (uint256.FromHex refuses them).
func normalizeHex(s string) string {
	digits := strings.TrimLeft(s[2:], "0")
	if digits == "" {
		digits = "0"
	}
	return "0x" + digits
}

func parseHash32(s string) (types.Hash, error) {
	raw, err := hexutil.Decode(s)
	if err != nil {
		return types.Hash{}, err
	}
	if len(raw) > 32 {
		return types.Hash{}, fmt.Errorf("%d bytes", len(raw))
	}
	return types.BytesToHash(raw), nil
}
