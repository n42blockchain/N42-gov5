package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type certifiedLookupOrderDB struct {
	kv.RwDB
	mu        sync.Mutex
	lookupKey [][]byte
	failKey   []byte
	failMode  string
	failErr   error
}

type certifiedLookupOrderTx struct {
	kv.Tx
	db *certifiedLookupOrderDB
}

func (d *certifiedLookupOrderDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	return d.RwDB.View(ctx, func(tx kv.Tx) error {
		return fn(&certifiedLookupOrderTx{Tx: tx, db: d})
	})
}

func (t *certifiedLookupOrderTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.TxLookup {
		t.db.mu.Lock()
		t.db.lookupKey = append(t.db.lookupKey, append([]byte(nil), key...))
		if bytes.Equal(key, t.db.failKey) && t.db.failMode != "" {
			mode, err := t.db.failMode, t.db.failErr
			t.db.mu.Unlock()
			switch mode {
			case "missing":
				return nil, nil
			case "wrong-value":
				return []byte{0xff}, nil
			case "error":
				return nil, err
			default:
				return nil, fmt.Errorf("unknown lookup fault mode %q", mode)
			}
		}
		t.db.mu.Unlock()
	}
	return t.Tx.GetOne(table, key)
}

func (d *certifiedLookupOrderDB) resetLookups() {
	d.mu.Lock()
	d.lookupKey = nil
	d.mu.Unlock()
}

func (d *certifiedLookupOrderDB) setFault(key []byte, mode string, err error) {
	d.mu.Lock()
	d.failKey = append(d.failKey[:0], key...)
	d.failMode, d.failErr = mode, err
	d.mu.Unlock()
}

func (d *certifiedLookupOrderDB) lookupKeys() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([][]byte, len(d.lookupKey))
	for i := range d.lookupKey {
		keys[i] = append([]byte(nil), d.lookupKey[i]...)
	}
	return keys
}

func sortedCertifiedLookupKeys(b *block.Block) [][]byte {
	keys := make([][]byte, 0, len(b.Transactions()))
	for _, txn := range b.Transactions() {
		keys = append(keys, txn.Hash().Bytes())
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys
}

func certifiedLookupKeysForRaws(t *testing.T, raws [][]byte) [][]byte {
	t.Helper()
	keys := make([][]byte, 0, len(raws))
	for _, raw := range raws {
		txn, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, txn.Hash().Bytes())
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys
}

func certifiedNonSortedInterleaving(t *testing.T, left, right [][]byte) [][]byte {
	t.Helper()
	if len(left) != 3 || len(right) != 3 {
		t.Fatalf("fixture streams have lengths %d/%d; want three transactions each", len(left), len(right))
	}
	all := append(append([][]byte(nil), left...), right...)
	want := certifiedLookupKeysForRaws(t, all)
	for mask := 0; mask < 1<<6; mask++ {
		if bits.OnesCount(uint(mask)) != len(left) {
			continue
		}
		li, ri := 0, 0
		var candidate [][]byte
		for i := 0; i < 6; i++ {
			if mask&(1<<i) != 0 {
				candidate = append(candidate, right[ri])
				ri++
			} else {
				candidate = append(candidate, left[li])
				li++
			}
		}
		keys := make([][]byte, 0, len(candidate))
		for _, raw := range candidate {
			txn, err := transaction.DecodeEthereumTransaction(raw)
			if err != nil {
				t.Fatal(err)
			}
			keys = append(keys, txn.Hash().Bytes())
		}
		if !equalCertifiedLookupKeys(keys, want) {
			return candidate
		}
	}
	t.Fatal("no valid transaction interleaving differs from sorted hash order")
	return nil
}

func equalCertifiedLookupKeys(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func certifiedCanonicalFixtureWithNonSortedHashes(t *testing.T, db *certifiedLookupOrderDB) (strictIngressFixture, *block.Block, CertifiedCommitRequest) {
	t.Helper()
	f := newStrictIngressFixtureWithDB(t, 10, 1<<20, func(base kv.RwDB) kv.RwDB {
		db.RwDB = base
		return db
	})
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatalf("admit fixture frame: %v", err)
		}
	}
	left, right := strictFrameRaws(t, f.frames[0]), strictFrameRaws(t, f.frames[1])
	b := strictExecute(t, f, certifiedNonSortedInterleaving(t, left, right))
	request := CertifiedCommitRequest{CommitRequest: CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64()}, Certificate: *commitProof(f.keys, b.Hash(), 5, false)}
	if _, err := f.server.CommitCertified(context.Background(), request); err != nil {
		t.Fatalf("commit non-sorted fixture: %v", err)
	}
	return f, b, request
}

func requireCertifiedLookupOrder(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("lookup count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("lookup %d = %x, want sorted transaction hash %x", i, got[i], want[i])
		}
	}
}

func TestCertifiedCanonicalExecutionLooksUpEveryTxBySortedHash(t *testing.T) {
	db := &certifiedLookupOrderDB{}
	f, b, request := certifiedCanonicalFixtureWithNonSortedHashes(t, db)
	if len(b.Transactions()) < 3 {
		t.Fatalf("fixture has %d transactions; need at least three", len(b.Transactions()))
	}
	want := sortedCertifiedLookupKeys(b)
	original := make([][]byte, 0, len(b.Transactions()))
	for _, txn := range b.Transactions() {
		original = append(original, txn.Hash().Bytes())
	}
	if len(original) < 2 || equalCertifiedLookupKeys(original, want) {
		t.Fatal("fixture transaction order unexpectedly matches sorted hash order")
	}

	db.resetLookups()
	proof, existing, err := f.chain.VerifyCertifiedCanonicalExecution(&request.Certificate)
	if err != nil || !existing || proof.Transactions != len(b.Transactions()) {
		t.Fatalf("fresh canonical execution proof: %+v existing=%t err=%v", proof, existing, err)
	}
	requireCertifiedLookupOrder(t, db.lookupKeys(), want)
}

func TestCertifiedCanonicalExecutionFreshlyRejectsSortedLookupMiddleFailure(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-value", "error"} {
		t.Run(mode, func(t *testing.T) {
			db := &certifiedLookupOrderDB{}
			f, b, request := certifiedCanonicalFixtureWithDB(t, true, func(base kv.RwDB) kv.RwDB {
				db.RwDB = base
				return db
			})
			want := sortedCertifiedLookupKeys(b)
			if len(want) < 3 {
				t.Fatalf("fixture has %d transactions; need at least three", len(want))
			}

			// Establish that the first physical request recognizes a valid index,
			// then invalidate its middle sorted key before the fresh verification.
			if _, existing, err := f.chain.VerifyCertifiedCanonicalExecution(&request.Certificate); err != nil || !existing {
				t.Fatalf("initial valid proof: existing=%t err=%v", existing, err)
			}
			db.resetLookups()
			middle := want[len(want)/2]
			if mode == "missing" || mode == "wrong-value" {
				if err := f.chain.DB().Update(context.Background(), func(tx kv.RwTx) error {
					if mode == "missing" {
						return tx.Delete(modules.TxLookup, middle)
					}
					return tx.Put(modules.TxLookup, middle, []byte{0xff})
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				db.setFault(middle, "error", errors.New("injected sorted lookup read error"))
			}

			proof, existing, err := f.chain.VerifyCertifiedCanonicalExecution(&request.Certificate)
			if mode == "error" {
				if !errors.Is(err, db.failErr) || existing {
					t.Fatalf("lookup error hidden by fresh proof: %+v existing=%t err=%v", proof, existing, err)
				}
			} else if err != nil || existing {
				t.Fatalf("invalid fresh lookup accepted: %+v existing=%t err=%v", proof, existing, err)
			}
			got := db.lookupKeys()
			requireCertifiedLookupOrder(t, got, want[:len(want)/2+1])
		})
	}
}
