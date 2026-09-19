// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// archive.go — the DATC reader as a library: EIP-1186 proofs of Ethereum state
// at any height of a finished archive, for a long-lived process (eth-el's
// public RPC, see internal/ethel/publicrpc). The n42-datc command reaches the
// same reader through proof/bench/verify.
//
// An archive is read-only here. The weekly update (docs/ethel/
// datc-weekly-update.md) rewrites it in place while a node may be serving it:
// segments are replaced by rename and the sidecars (ns.ladders, a.stages)
// likewise, the MDBX file gains rows. Open files keep their old inodes, so a
// reader never sees a half-written segment; Archive notices the new generation
// (head, sidecars) and builds fresh readers for later requests.
package datc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// ErrNotCovered: the archive holds no state for the requested height (above
// its head, or below the start of a partial archive). Callers fall back to
// whatever else can answer.
var ErrNotCovered = errors.New("datc: height not covered by the archive")

// ErrProofMismatch: a proof assembled from the archive does not verify
// against the state root it was checked with. The archive is wrong for that
// height (or the root belongs to another chain); nothing may be served.
var ErrProofMismatch = errors.New("datc: proof does not verify against the state root")

// ArchiveOptions tunes a reader. Zero values are the defaults.
type ArchiveOptions struct {
	// MapGB is the MDBX map size; it must cover the file (default 2048).
	MapGB int
	// FrameCache is the number of decompressed segment frames each pooled
	// reader keeps (default 256; 16-64 KiB each).
	FrameCache int
	// Readers bounds the pooled readers, i.e. concurrent proofs (default 16).
	Readers int
	// RefreshEvery is how often the archive is checked for a weekly update
	// (default 30 s; negative disables).
	RefreshEvery time.Duration
}

// Archive is an open DATC archive.
type Archive struct {
	dir  string
	db   kv.RoDB
	opts ArchiveOptions

	mu        sync.Mutex
	gen       uint64 // bumped when the archive on disk changes
	stamp     string // what gen was computed from
	head      uint64 // first block NOT covered
	start     uint64 // first block covered (0 for a complete archive)
	checkedAt time.Time
	hold      *segHold // the current generation's segments, kept loaded

	pool chan *pooledReader
}

type pooledReader struct {
	gen uint64
	tx  kv.Tx
	q   *querier
}

// OpenArchive opens the archive in dir read-only. It never touches process-wide
// table configuration, so it can live inside a node that has its own MDBX.
func OpenArchive(dir string, o ArchiveOptions) (*Archive, error) {
	if o.MapGB <= 0 {
		o.MapGB = 2048
	}
	if o.FrameCache <= 0 {
		o.FrameCache = 256
	}
	if o.Readers <= 0 {
		o.Readers = 16
	}
	if o.RefreshEvery == 0 {
		o.RefreshEvery = 30 * time.Second
	}
	if _, err := os.Stat(filepath.Join(dir, "mdbx.dat")); err != nil {
		return nil, fmt.Errorf("datc: %s is not an archive: %w", dir, err)
	}
	db, err := openArchiveDB(dir, o.MapGB)
	if err != nil {
		return nil, fmt.Errorf("datc: open %s: %w", dir, err)
	}
	a := &Archive{dir: dir, db: db, opts: o, pool: make(chan *pooledReader, o.Readers)}
	if err := a.refresh(true); err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}

// openArchiveDB opens the archive's MDBX read-only with the DATC tables
// registered on a private copy of the chaindata schema.
func openArchiveDB(dir string, mapGB int) (kv.RoDB, error) {
	modules.N42Init() // idempotent: completes modules.N42TableCfg
	return mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(mapGB) * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for name, item := range modules.N42TableCfg {
				d[name] = item
			}
			for _, t := range datcTables {
				d[t] = kv.TableCfgItem{}
			}
			return d
		}).Accede().Readonly().Open(context.Background())
}

// Dir is the archive directory.
func (a *Archive) Dir() string { return a.dir }

// Range is the block range the archive proves: [start, head).
func (a *Archive) Range() (start, head uint64) {
	_ = a.refresh(false)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.start, a.head
}

// Close releases the pooled readers and the database. Call it after serving
// has stopped: the database closes only when no proof is in flight.
func (a *Archive) Close() {
	for {
		select {
		case r := <-a.pool:
			r.close()
		default:
			a.mu.Lock()
			if a.hold != nil {
				a.hold.release()
				a.hold = nil
			}
			a.mu.Unlock()
			a.db.Close()
			return
		}
	}
}

func (r *pooledReader) close() {
	r.q.Close()
	r.tx.Rollback()
}

// stampOf fingerprints what a weekly update changes: the sidecars and the
// segment directory (a renamed-in segment updates its mtime).
func (a *Archive) stampOf() string {
	s := ""
	for _, p := range []string{leafSegDir, filepath.Join(leafSegDir, exactLaddersFile), filepath.Join(leafSegDir, accStagesFile)} {
		if st, err := os.Stat(filepath.Join(a.dir, p)); err == nil {
			s += fmt.Sprintf("%s:%d:%d;", p, st.Size(), st.ModTime().UnixNano())
		}
	}
	return s
}

// refresh re-reads head/start and bumps the generation when the archive
// changed. Rate-limited unless force.
func (a *Archive) refresh(force bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !force && (a.opts.RefreshEvery < 0 || time.Since(a.checkedAt) < a.opts.RefreshEvery) {
		return nil
	}
	a.checkedAt = time.Now()
	stamp := a.stampOf()
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return fmt.Errorf("datc: begin: %w", err)
	}
	defer tx.Rollback()
	q, head, err := loadQuerierCache(tx, a.dir, 0, 8)
	if err != nil {
		return fmt.Errorf("datc: %s: %w", a.dir, err)
	}
	q.Close()
	var start uint64
	if v, err := tx.GetOne(tDatcMeta, []byte("start")); err == nil && len(v) == 8 {
		start = beUint64(v)
	}
	if start > 0 {
		// A partial archive has no rows for keys untouched since its base, at
		// ANY height: it is only right together with that base (n42-datc
		// --base). Merge it first; never serve it alone.
		return fmt.Errorf("datc: %s is a partial archive (built from block %d): merge it before serving", a.dir, start)
	}
	if force || stamp != a.stamp || head != a.head || start != a.start {
		// Load the new generation's segment indexes before letting go of the
		// old ones: segments the update did not touch stay loaded throughout.
		hold, err := holdSegFiles(a.dir, 32)
		if err != nil {
			return fmt.Errorf("datc: segments: %w", err)
		}
		if a.hold != nil {
			a.hold.release()
		}
		a.hold = hold
		if !force {
			log.Info("datc: archive changed on disk, new readers from now on", "dir", a.dir, "head", head)
		}
		a.gen++
		a.stamp, a.head, a.start = stamp, head, start
	}
	return nil
}

func beUint64(b []byte) uint64 {
	var v uint64
	for _, x := range b[:8] {
		v = v<<8 | uint64(x)
	}
	return v
}

// reader takes a pooled reader of the current generation (or builds one).
func (a *Archive) reader() (*pooledReader, error) {
	a.mu.Lock()
	gen := a.gen
	a.mu.Unlock()
	for {
		select {
		case r := <-a.pool:
			if r.gen == gen {
				return r, nil
			}
			r.close() // built before a weekly update
			continue
		default:
		}
		break
	}
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	q, _, err := loadQuerierCache(tx, a.dir, 0, a.opts.FrameCache)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	// Serving verifies against the header root (or leaves that to the client);
	// the builder-level cross-check would hash every fold twice.
	q.crossCheck = false
	return &pooledReader{gen: gen, tx: tx, q: q}, nil
}

func (a *Archive) release(r *pooledReader) {
	a.mu.Lock()
	stale := r.gen != a.gen
	a.mu.Unlock()
	if stale {
		r.close()
		return
	}
	select {
	case a.pool <- r:
	default:
		r.close()
	}
}

// Proof is one eth_getProof answer (EIP-1186) as of a block's post-state.
type Proof struct {
	Block        uint64
	Address      types.Address
	Exists       bool // the account exists at Block
	Nonce        uint64
	Balance      *uint256.Int
	CodeHash     types.Hash
	StorageHash  types.Hash
	AccountProof [][]byte // RLP trie nodes, root first
	Storage      []SlotProof
}

// SlotProof is the proof of one storage slot.
type SlotProof struct {
	Key   types.Hash // the slot as requested (not hashed)
	Value *uint256.Int
	Proof [][]byte
}

// emptyCodeHash is keccak256("") — the code hash EIP-1186 reports for an
// account without code, and for one that does not exist.
var emptyCodeHash = keccak(nil)

// Prove assembles the proof of address (and slots) as of the post-state of
// block n. With stateRoot non-nil every proof is walked from that root first
// and ErrProofMismatch is returned if one does not verify or disagrees with
// the value the archive holds — the node serves nothing it has not checked.
func (a *Archive) Prove(ctx context.Context, address types.Address, slots []types.Hash, n uint64, stateRoot *types.Hash) (*Proof, error) {
	_ = a.refresh(false)
	a.mu.Lock()
	start, head := a.start, a.head
	a.mu.Unlock()
	if n >= head || n < start {
		return nil, fmt.Errorf("%w: block %d, archive [%d, %d)", ErrNotCovered, n, start, head)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := a.reader()
	if err != nil {
		return nil, err
	}
	p, err := proveWith(r.q, address, slots, n, stateRoot)
	if err != nil && !errors.Is(err, ErrProofMismatch) {
		r.close() // a reader error may leave cursors in any state
		return nil, err
	}
	a.release(r)
	return p, err
}

func proveWith(q *querier, address types.Address, slots []types.Hash, n uint64, stateRoot *types.Hash) (*Proof, error) {
	ah := keccak(address[:])
	accNib := nibblesOfBytes(ah[:])
	out := &Proof{Block: n, Address: address, Balance: new(uint256.Int), CodeHash: emptyCodeHash, StorageHash: emptyTrieRoot}
	var err error
	if out.AccountProof, err = q.proofPath(nil, accNib, n); err != nil {
		return nil, fmt.Errorf("account proof: %w", err)
	}
	raw, live, err := q.leafFloor(false, ah[:], n)
	if err != nil {
		return nil, fmt.Errorf("account leaf: %w", err)
	}
	if live {
		var acct account.StateAccount
		if err := acct.DecodeForStorage(raw); err != nil {
			return nil, fmt.Errorf("account decode: %w", err)
		}
		out.Exists = true
		out.Nonce = acct.Nonce
		out.Balance = acct.Balance.Clone()
		if acct.CodeHash != (types.Hash{}) {
			out.CodeHash = acct.CodeHash
		}
		sroot, has, err := q.nodeHashAt(ah[:], nil, n)
		if err != nil {
			return nil, fmt.Errorf("storage root: %w", err)
		}
		if has {
			out.StorageHash = sroot
		}
		if stateRoot != nil {
			got, err := walkProof(*stateRoot, out.AccountProof, accNib)
			if err != nil {
				return out, fmt.Errorf("%w: account %x at %d: %v", ErrProofMismatch, address[:4], n, err)
			}
			acct.Root = out.StorageHash
			buf := make([]byte, acct.EncodingLengthForHashing())
			acct.EncodeForHashing(buf)
			if string(got) != string(buf) {
				return out, fmt.Errorf("%w: account %x at %d: leaf differs from the archive's value", ErrProofMismatch, address[:4], n)
			}
		}
	} else if stateRoot != nil {
		if got, err := walkProof(*stateRoot, out.AccountProof, accNib); err != nil || got != nil {
			return out, fmt.Errorf("%w: account %x at %d: absence not proven (%v)", ErrProofMismatch, address[:4], n, err)
		}
	}
	for _, slot := range slots {
		sp := SlotProof{Key: slot, Value: new(uint256.Int)}
		if out.Exists && out.StorageHash != emptyTrieRoot {
			sh := keccak(slot[:])
			sNib := nibblesOfBytes(sh[:])
			if sp.Proof, err = q.proofPath(ah[:], sNib, n); err != nil {
				return nil, fmt.Errorf("slot %x proof: %w", slot[:4], err)
			}
			composite := append(append(make([]byte, 0, 64), ah[:]...), sh[:]...)
			val, slive, err := q.leafFloor(true, composite, n)
			if err != nil {
				return nil, fmt.Errorf("slot %x leaf: %w", slot[:4], err)
			}
			if slive {
				sp.Value.SetBytes(val)
			}
			if stateRoot != nil {
				got, err := walkProof(out.StorageHash, sp.Proof, sNib)
				switch {
				case err != nil:
					return out, fmt.Errorf("%w: slot %x at %d: %v", ErrProofMismatch, slot[:4], n, err)
				case slive && string(got) != string(rlpStr(val)):
					return out, fmt.Errorf("%w: slot %x at %d: value differs from the archive's", ErrProofMismatch, slot[:4], n)
				case !slive && got != nil:
					return out, fmt.Errorf("%w: slot %x at %d: absence not proven", ErrProofMismatch, slot[:4], n)
				}
			}
		}
		// An account without storage proves every slot absent through the
		// account proof itself (its storage root is the empty root); EIP-1186
		// then returns an empty proof list.
		out.Storage = append(out.Storage, sp)
	}
	return out, nil
}
