package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"lukechampine.com/blake3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

const maxAuditSlots = 1_000_000

type leafCandidate struct {
	TwigID uint64 `json:"twig_id"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type forestReport struct {
	NextSlot       uint64          `json:"next_slot"`
	GenesisSlots   uint64          `json:"genesis_slots"`
	LiveKeys       uint64          `json:"live_keys"`
	Root           types.Hash      `json:"root"`
	MissingGenesis []leafCandidate `json:"missing_genesis_leaf_blobs"`
}

type leafOverlay struct {
	base qmdb.Getter
	rows map[string][]byte
}

func (g leafOverlay) GetOne(table string, key []byte) ([]byte, error) {
	if table == qmdb.LeavesTable {
		if row, ok := g.rows[string(key)]; ok {
			return row, nil
		}
	}
	if g.base == nil {
		return nil, errors.New("unexpected overlay read")
	}
	return g.base.GetOne(table, key)
}

func readCursor(g qmdb.Getter, name string) (uint64, error) {
	v, err := g.GetOne(qmdb.MetaTable, []byte(name))
	if err != nil {
		return 0, err
	}
	if len(v) != 8 {
		return 0, fmt.Errorf("invalid QMDB %s length %d", name, len(v))
	}
	n := binary.BigEndian.Uint64(v)
	if n == 0 || n > maxAuditSlots {
		return 0, fmt.Errorf("QMDB %s outside bounded audit range", name)
	}
	return n, nil
}

func position(n uint64) []byte {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], n)
	return key[:]
}

func pairHash(left, right []byte) [32]byte {
	var pair [64]byte
	copy(pair[:32], left)
	copy(pair[32:], right)
	return blake3.Sum256(pair[:])
}

func blobRoot(raw []byte) ([]byte, [32]byte, error) {
	// Decode through the actual raw/sparse reader, with a getter whose only
	// value has already been read and checked for storage errors.
	g := leafOverlay{rows: map[string][]byte{string(position(0)): raw}}
	leaves, ok := qmdb.LeafStoreFromGetter(g).Leaves(0)
	if !ok || len(leaves) != qmdb.TwigSize*32 {
		return nil, [32]byte{}, errors.New("malformed frozen leaf blob")
	}
	nodes := append([]byte(nil), leaves...)
	for width := qmdb.TwigSize; width > 1; width /= 2 {
		for i := 0; i < width/2; i++ {
			h := pairHash(nodes[i*64:i*64+32], nodes[i*64+32:i*64+64])
			copy(nodes[i*32:i*32+32], h[:])
		}
	}
	var root [32]byte
	copy(root[:], nodes[:32])
	return leaves, root, nil
}

type liveValue struct {
	slot  uint64
	value []byte
}

// auditForest never writes to tx. Only missing, fully sealed genesis blobs
// enter its temporary overlay; every current commitment must reproduce exactly.
func auditForest(tx kv.Tx, genesis qmdb.Getter, expected types.Hash) (forestReport, error) {
	var out forestReport
	next, err := readCursor(tx, "nextSlot")
	if err != nil {
		return out, err
	}
	genesisSlots, err := readCursor(genesis, "nextSlot")
	if err != nil || genesisSlots > next {
		return out, errors.New("invalid genesis/current positional boundary")
	}
	liveCount, err := readCursor(tx, "liveCount")
	if err != nil || liveCount > next {
		return out, errors.New("invalid persisted live count")
	}
	out.NextSlot, out.GenesisSlots = next, genesisSlots
	for table, limit := range map[string]uint64{
		qmdb.TwigTable:   (next + qmdb.TwigSize - 1) / qmdb.TwigSize,
		qmdb.LeavesTable: (next + qmdb.TwigSize - 1) / qmdb.TwigSize,
		qmdb.EntryTable:  next,
	} {
		if err := auditPositions(tx, table, limit); err != nil {
			return out, err
		}
	}
	storedRoot, err := tx.GetOne(qmdb.MetaTable, []byte("root"))
	if err != nil {
		return out, err
	}
	if len(storedRoot) != 32 || !bytes.Equal(storedRoot, expected[:]) {
		return out, errors.New("persisted QMDB root differs from applied header")
	}
	overlay := leafOverlay{base: tx, rows: make(map[string][]byte)}
	live := make(map[qmdb.Hash]liveValue)
	for id := uint64(0); id < (next+qmdb.TwigSize-1)/qmdb.TwigSize; id++ {
		key := position(id)
		meta, err := tx.GetOne(qmdb.TwigTable, key)
		if err != nil {
			return out, err
		}
		if len(meta) != 64+qmdb.TwigSize/8 {
			return out, fmt.Errorf("twig %d has invalid split metadata", id)
		}
		raw, err := tx.GetOne(qmdb.LeavesTable, key)
		if err != nil {
			return out, err
		}
		if len(raw) == 0 {
			exists, err := tx.Has(qmdb.LeavesTable, key)
			if err != nil {
				return out, err
			}
			if exists {
				return out, fmt.Errorf("existing empty frozen blob %d", id)
			}
			if id >= genesisSlots/qmdb.TwigSize {
				return out, fmt.Errorf("missing non-genesis frozen blob %d", id)
			}
			raw, err = genesis.GetOne(qmdb.LeavesTable, key)
			if err != nil {
				return out, err
			}
			digest := sha256.Sum256(raw)
			out.MissingGenesis = append(out.MissingGenesis, leafCandidate{id, len(raw), hex.EncodeToString(digest[:])})
			overlay.rows[string(key)] = append([]byte(nil), raw...)
		}
		leaves, leafRoot, err := blobRoot(raw)
		if err != nil {
			return out, fmt.Errorf("twig %d: %w", id, err)
		}
		bitsRoot := blake3.Sum256(append([]byte{3}, meta[64:]...))
		root := pairHash(leafRoot[:], bitsRoot[:])
		if !bytes.Equal(leafRoot[:], meta[32:64]) || !bytes.Equal(root[:], meta[:32]) {
			return out, fmt.Errorf("twig %d frozen leaf or bitmap commitment differs", id)
		}
		for local := uint64(0); local < qmdb.TwigSize; local++ {
			slot := id*qmdb.TwigSize + local
			if slot >= next && !bytes.Equal(leaves[local*32:(local+1)*32], make([]byte, 32)) {
				return out, errors.New("nonempty frozen leaf beyond append cursor")
			}
			if meta[64+local/8]&(1<<(local%8)) == 0 {
				continue
			}
			if slot >= next {
				return out, errors.New("live bit beyond append cursor")
			}
			entry, err := tx.GetOne(qmdb.EntryTable, position(slot))
			if err != nil {
				return out, err
			}
			if len(entry) <= 32 || len(entry) > 4096 {
				return out, fmt.Errorf("live entry %d missing or malformed", slot)
			}
			h := blake3.Sum256(append([]byte{1}, entry...))
			if !bytes.Equal(h[:], leaves[local*32:(local+1)*32]) {
				return out, fmt.Errorf("live entry %d does not match committed leaf", slot)
			}
			var kh qmdb.Hash
			copy(kh[:], entry[:32])
			if _, duplicate := live[kh]; duplicate {
				return out, errors.New("multiple active slots for one key")
			}
			live[kh] = liveValue{slot, append([]byte(nil), entry[32:]...)}
		}
	}
	tree := qmdb.New()
	tree.SetIndex(qmdb.NewMapIndex()) // never trust environment-selected truncation
	tree.SetCold(qmdb.ColdReaderFromGetter(overlay))
	tree.SetLeafStore(qmdb.LeafStoreFromGetter(overlay))
	if err := tree.LoadFrom(overlay); err != nil {
		return out, err
	}
	if types.Hash(tree.Root()) != expected || uint64(len(live)) != liveCount || tree.LiveCount() != len(live) || tree.LiveBits() != len(live) {
		return out, errors.New("forest root or live populations disagree")
	}
	for kh, v := range live {
		if slot, ok := tree.IndexLookup(kh); !ok || slot != v.slot {
			return out, errors.New("rebuilt index points at a different slot")
		}
	}
	if err := auditPlainState(tx, live); err != nil {
		return out, err
	}
	out.LiveKeys, out.Root = liveCount, expected
	return out, nil
}

func auditPositions(tx kv.Tx, table string, limit uint64) error {
	cursor, err := tx.Cursor(table)
	if err != nil {
		return err
	}
	defer cursor.Close()
	key, _, err := cursor.First()
	for key != nil && err == nil {
		if len(key) != 8 || binary.BigEndian.Uint64(key) >= limit {
			return fmt.Errorf("invalid positional key in %s", table)
		}
		key, _, err = cursor.Next()
	}
	return err
}

func auditPlainState(tx kv.Tx, live map[qmdb.Hash]liveValue) error {
	for _, table := range []string{modules.Account, modules.Storage} {
		cursor, err := tx.Cursor(table)
		if err != nil {
			return err
		}
		err = func() error {
			defer cursor.Close()
			key, value, err := cursor.First()
			for key != nil && err == nil {
				var kh qmdb.Hash
				var encoded []byte
				if table == modules.Account {
					if len(key) != 20 {
						return errors.New("invalid plain account key")
					}
					var acct account.StateAccount
					if err := acct.DecodeForStorage(value); err != nil {
						return err
					}
					if acct.Nonce == 0 && acct.Balance.IsZero() && !acct.Initialised {
						key, value, err = cursor.Next()
						continue
					}
					kh = qmdb.Hash(commitment.AccountKeyHash(types.BytesToAddress(key)))
					encoded = commitment.EncodeAccountValue(&acct)
				} else {
					if len(key) != 52 || len(value) == 0 || len(value) > 32 {
						return errors.New("unsupported plain storage key/value")
					}
					kh = qmdb.Hash(commitment.StorageKeyHash(types.BytesToAddress(key[:20]), types.BytesToHash(key[20:])))
					encoded = make([]byte, 32)
					copy(encoded[32-len(value):], value)
					if bytes.Equal(encoded, make([]byte, 32)) {
						return errors.New("zero plain storage row")
					}
				}
				got, ok := live[kh]
				if !ok || !bytes.Equal(got.value, encoded) {
					return errors.New("plain state does not match committed live value")
				}
				delete(live, kh)
				key, value, err = cursor.Next()
			}
			return err
		}()
		if err != nil {
			return err
		}
	}
	if len(live) != 0 {
		return unmatchedPlainState(live)
	}
	return nil
}
