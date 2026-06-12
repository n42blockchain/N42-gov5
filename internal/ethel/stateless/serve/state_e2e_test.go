package serve

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// stateBE is a Backend over a REAL state-changing forward chain (the shape of
// replaying acctcs/storcs through MerkleStageIncremental): each anchor height
// carries the block's genuine pre-state MPT multiproof + changes, and /code
// serves a deployed bytecode. This is the IDC-serves-real-data ↔ phone-verifies
// path that chainBE (empty state) and producer_loop (no HTTP) each cover only
// half of.
type stateBE struct {
	chainBE
	anchors map[uint64]*stateless.BlockProof
	codes   map[types.Hash][]byte
}

func (b *stateBE) Anchor(n uint64) ([]byte, error) {
	bp, ok := b.anchors[n]
	if !ok {
		return nil, errors.New("not an anchor height")
	}
	return stateless.EncodeBlockProof(bp), nil
}

func (b *stateBE) Code(h types.Hash) ([]byte, error) {
	c, ok := b.codes[h]
	if !ok {
		return nil, http.ErrNoLocation
	}
	return c, nil
}

func addr20b(i uint64) types.Address {
	var a types.Address
	binary.BigEndian.PutUint64(a[12:], i)
	return a
}

// TestHTTPStateAnchorEndToEnd: a minimal client syncs a real state-changing
// chain over the HTTP transport, fetching headers (①) and REAL pre-state MPT
// anchors (③) from the serve Handler and verifying each anchor against the
// header chain, plus a /code round-trip (the phone's on-demand bytecode fetch).
// This is the production IDC-serve ↔ minimal-client regression on non-empty
// state.
func TestHTTPStateAnchorEndToEnd(t *testing.T) {
	const N = 12
	const K = uint64(4)
	emptyCodeHash := crypto.Keccak256(nil)

	// Build a 40-account base state, then advance the trie block by block,
	// capturing the genuine pre-state multiproof at each anchor.
	accts := map[types.Address]*account.StateAccount{}
	for i := 1; i <= 40; i++ {
		a := &account.StateAccount{}
		a.Reset()
		a.Nonce = uint64(i)
		a.Balance.SetUint64(uint64(i) * 1000)
		a.CodeHash = types.BytesToHash(emptyCodeHash)
		a.Initialised = true
		accts[addr20b(uint64(i))] = a
	}

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	r0, err := trc.ComputeRoot(accts, nil)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	headers := []*block.Header{mkH(0, types.Hash{}, r0)}
	anchors := map[uint64]*stateless.BlockProof{}

	trc.SetIncremental(true)
	for a := uint64(1); a <= N; a++ {
		dA := map[types.Address]*account.StateAccount{}
		for _, mul := range []uint64{7, 13, 5} {
			ad := addr20b((a*mul)%40 + 1)
			na := *accts[ad]
			na.Balance.SetUint64(1_000_000 + a)
			na.Nonce = a
			dA[ad] = &na
		}
		touched := make([]types.Address, 0, len(dA))
		for ad := range dA {
			touched = append(touched, ad)
		}
		// Write the changeset so BuildRetainListFromChangesets recovers the
		// touched keys (value = the OLD/pre-state encoding).
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], a)
		for _, ad := range touched {
			v := append(append([]byte(nil), ad[:]...), accts[ad].MarshalV2()...)
			if err := tx.Put("AccountChangeSet", bk[:], v); err != nil {
				t.Fatalf("changeset %d: %v", a, err)
			}
		}

		isAnchor := a%K == 0
		if isAnchor {
			preRoot, pp, perr := commitment.ExtractBlockMultiproof(tx, a, a)
			if perr != nil {
				t.Fatalf("extract %d: %v", a, perr)
			}
			if preRoot != headers[a-1].Root {
				t.Fatalf("anchor %d preRoot %x != header[%d].Root %x", a, preRoot[:4], a-1, headers[a-1].Root[:4])
			}
			var changes []stateless.AccountChange
			for ad, na := range dA {
				ch := stateless.AccountChange{
					AddrHash: types.BytesToHash(crypto.Keccak256(ad[:])),
					Nonce:    na.Nonce,
					CodeHash: emptyCodeHash,
				}
				ch.Balance.Set(&na.Balance)
				changes = append(changes, ch)
			}
			anchors[a] = &stateless.BlockProof{Number: a, AccountProof: pp, Changes: changes}
		}

		rn, err := trc.ComputeRoot(dA, nil)
		if err != nil {
			t.Fatalf("apply %d: %v", a, err)
		}
		for k, v := range dA {
			accts[k] = v
		}
		headers = append(headers, mkH(a, headers[a-1].Hash(), rn))
	}

	// Serve it; a phone-shaped minimal client syncs over HTTP.
	code := []byte{0x60, 0x60, 0x60, 0x40}
	codeHash := types.BytesToHash(crypto.Keccak256(code))
	be := &stateBE{
		chainBE: chainBE{headers: headers, anchorEvery: K},
		anchors: anchors,
		codes:   map[types.Hash][]byte{codeHash: code},
	}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	src := NewHTTPSource(srv.URL)
	mc, err := stateless.NewMinimalClient(src, headers[0], 1000, K)
	if err != nil {
		t.Fatal(err)
	}
	head, err := mc.Sync()
	if err != nil {
		t.Fatalf("sync real-state chain over HTTP: %v", err)
	}
	if head != N {
		t.Fatalf("synced head %d != tip %d", head, N)
	}

	// /code round-trip: the phone fetches a deployed bytecode and verifies it.
	got, err := src.Code(codeHash)
	if err != nil {
		t.Fatalf("fetch code: %v", err)
	}
	if string(got) != string(code) {
		t.Fatalf("code round-trip mismatch")
	}
	if types.BytesToHash(crypto.Keccak256(got)) != codeHash {
		t.Fatalf("fetched code fails keccak self-check")
	}
}
