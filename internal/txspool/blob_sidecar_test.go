package txspool

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func osakaTestConfigs() (pre, osaka *params.ChainConfig) {
	o := *params.TestChainConfig
	o.OsakaTime = big.NewInt(0)
	o.GlamsterdamTime = nil
	p := o
	p.OsakaTime = nil
	return &p, &o
}

func newPoolBlobTx(from types.Address, nonce uint64, version byte) *transaction.Transaction {
	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(10),
		Gas:        21_000,
		To:         from,
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{{0x01, byte(nonce)}},
		Sidecar: &transaction.BlobTxSidecar{
			Version:     version,
			Blobs:       make([]transaction.Blob, 1),
			Commitments: make([]transaction.Commitment, 1),
			Proofs:      make([]transaction.Proof, 1),
		},
	})
	tx.SetFrom(from)
	return tx
}

// From Osaka the pool refuses version-0 sidecars; before it, and for
// version-1 sidecars, the sidecar version is no reason to refuse.
func TestValidateTx_LegacyBlobSidecarByFork(t *testing.T) {
	pre, osaka := osakaTestConfigs()
	for _, tc := range []struct {
		name    string
		config  *params.ChainConfig
		version byte
		refused bool
	}{
		{"pre-osaka v0", pre, transaction.BlobSidecarVersion0, false},
		{"osaka v0", osaka, transaction.BlobSidecarVersion0, true},
		{"osaka v1", osaka, transaction.BlobSidecarVersion1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := g49NewValidationPool()
			pool.chainconfig = tc.config
			_, from := generateTestKey()
			pool.currentState.(*mockReadState).balances[from] = new(uint256.Int).Lsh(uint256.NewInt(1), 100)
			err := pool.validateTx(newPoolBlobTx(from, 0, tc.version), true, nil)
			if got := errors.Is(err, ErrLegacyBlobSidecar); got != tc.refused {
				t.Fatalf("err = %v, refused %v, want %v", err, got, tc.refused)
			}
		})
	}
}

// At the first reset onto an Osaka head the pool converts its version-0
// sidecars: pending and queued transactions are swapped for converted
// copies everywhere the pool holds them, the originals are left untouched
// for readers that still hold them, and a sidecar that does not convert
// takes its transaction out of the pool.
func TestConvertLegacySidecarsAtOsaka(t *testing.T) {
	pre, osaka := osakaTestConfigs()
	prev := convertSidecar
	t.Cleanup(func() { convertSidecar = prev })
	convertSidecar = func(sc *transaction.BlobTxSidecar) error {
		if sc.Blobs[0][0] == 0xff {
			return errors.New("bad blob")
		}
		sc.Version = transaction.BlobSidecarVersion1
		sc.Proofs = make([]transaction.Proof, transaction.CellProofsPerBlob)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := g49NewValidationPool()
	pool.ctx = ctx
	pool.chainconfig = pre
	pool.locals = newAccountSet()
	pool.pending = map[types.Address]*txsList{}
	pool.queue = map[types.Address]*txsList{}
	pool.beats = map[types.Address]time.Time{}
	pool.all = newTxLookup()
	pool.priced = newTxPricedList(pool.all)
	pool.pendingNonces = newTxNoncer(pool.currentState)

	a, b, c := types.Address{0x0a}, types.Address{0x0b}, types.Address{0x0c}
	pending := newPoolBlobTx(a, 0, transaction.BlobSidecarVersion0)
	queued := newPoolBlobTx(b, 5, transaction.BlobSidecarVersion0)
	broken := newPoolBlobTx(c, 0, transaction.BlobSidecarVersion0)
	broken.BlobTxSidecar().Blobs[0][0] = 0xff
	current := newPoolBlobTx(c, 1, transaction.BlobSidecarVersion1)
	put := func(lists map[types.Address]*txsList, tx *transaction.Transaction) {
		from := *tx.From()
		if lists[from] == nil {
			lists[from] = newTxsList(true)
		}
		lists[from].Add(tx, 0)
		pool.all.Add(tx, false)
		pool.priced.Put(tx, false)
	}
	put(pool.pending, pending)
	put(pool.queue, queued)
	put(pool.pending, broken)
	put(pool.pending, current)
	pool.publishPendingSnapshot()

	// Not yet Osaka: nothing to do, and the one conversion is not spent.
	pool.maybeConvertLegacySidecars(pool.bc.CurrentBlock())
	pool.wg.Wait()
	if pool.legacySidecarsConverted.Load() {
		t.Fatal("conversion ran before Osaka")
	}

	pool.chainconfig = osaka
	pool.maybeConvertLegacySidecars(pool.bc.CurrentBlock())
	pool.wg.Wait()

	for _, tc := range []struct {
		name string
		orig *transaction.Transaction
		list *txsList
	}{
		{"pending", pending, pool.pending[a]},
		{"queued", queued, pool.queue[b]},
	} {
		got := pool.all.Get(tc.orig.Hash())
		if got == nil || got == tc.orig || got.BlobTxSidecar().Version != transaction.BlobSidecarVersion1 {
			t.Fatalf("%s: lookup holds %v", tc.name, got)
		}
		if tc.list.txs.Get(tc.orig.Nonce()) != got {
			t.Fatalf("%s: list not swapped", tc.name)
		}
		if *got.From() != *tc.orig.From() || got.Hash() != tc.orig.Hash() {
			t.Fatalf("%s: sender or hash changed", tc.name)
		}
		if tc.orig.BlobTxSidecar().Version != transaction.BlobSidecarVersion0 || len(tc.orig.BlobTxSidecar().Proofs) != 1 {
			t.Fatalf("%s: original sidecar modified", tc.name)
		}
	}
	snap := *pool.pendingSnap.Load()
	if len(snap[a]) != 1 || snap[a][0].BlobTxSidecar().Version != transaction.BlobSidecarVersion1 {
		t.Fatalf("pending snapshot not republished: %v", snap[a])
	}
	if pool.all.Get(broken.Hash()) != nil {
		t.Fatal("unconvertible transaction still pooled")
	}
	if pool.all.Get(current.Hash()) != current {
		t.Fatal("version-1 transaction replaced")
	}
	for _, tx := range pool.priced.urgent.list {
		if sc := tx.BlobTxSidecar(); sc != nil && sc.Version == transaction.BlobSidecarVersion0 {
			t.Fatalf("priced list still holds the version-0 %x", tx.Hash())
		}
	}

	// Once only: a later version-0 transaction is not picked up (validateTx
	// refuses them from Osaka).
	late := newPoolBlobTx(types.Address{0x0d}, 0, transaction.BlobSidecarVersion0)
	put(pool.pending, late)
	pool.maybeConvertLegacySidecars(pool.bc.CurrentBlock())
	pool.wg.Wait()
	if pool.all.Get(late.Hash()) != late {
		t.Fatal("conversion ran twice")
	}
}
