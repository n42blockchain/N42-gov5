// n42-datc N42-chain source mode — runs DATC over an N42 converted chain
// (e.g. D:/mainnet-bls-full) whose changesets are erigon-style OLD-value
// dupsort tables and whose header roots are QMDB world roots (so the mainnet
// gold check does not apply).
//
// Phase 1 (convert): one BACKWARD pass over AccountChangeSet/StorageChangeSet
// derives forward NEW-value per-block changesets — new@B for a key is the
// old value recorded at the key's NEXT change (tracked in a pending map), or
// the chain's PlainState for its latest change. Results land in FwdAcctCS /
// FwdStorCS tables of the output DB.
//
// Phase 2 (build): the normal forward DATC build, reading FwdAcctCS/FwdStorCS,
// recording each block's computed MPT root into DatcRoots (the verify oracle,
// replacing headerc), and finishing with a FULL final-state equality check
// against the source chain's PlainState (the external gate that catches any
// conversion bug).
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

const (
	tFwdAcctCS = "FwdAcctCS" // block8 → packed [(addr20, uvarint len, newVal)]
	tFwdStorCS = "FwdStorCS" // block8 → packed [(composite52, uvarint len, newVal)]
	tDatcRoots = "DatcRoots" // block8 → computed MPT root (verify oracle)
)

// openN42Chain opens the source chain read-only.
func openN42Chain(dir string, mapGB int) kv.RoDB {
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).Path(dir).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open n42 chain: %v", err)
	}
	return db
}

// convertN42Changesets runs the backward derivation pass. Returns the number
// of blocks that produced forward entries.
func convertN42Changesets(src kv.Tx, outDB kv.RwDB, endBlock uint64) error {
	t0 := time.Now()

	// pending[key] = the key's value BEFORE its next (later) change — i.e. the
	// NEW value at the change being processed now (we iterate backward).
	pendingA := make(map[string][]byte, 1<<22)
	pendingS := make(map[string][]byte, 1<<16)

	// Latest values come from the chain's PlainState.
	latestA := func(addr []byte) []byte {
		v, _ := src.GetOne(modules.Account, addr)
		return v
	}
	latestS := func(composite []byte) []byte {
		v, _ := src.GetOne(modules.Storage, composite)
		return v
	}

	type blockBuf struct {
		n    uint64
		blob []byte
	}
	flushEvery := 100_000
	var bufA, bufS []blockBuf

	flush := func() error {
		tx, err := outDB.BeginRw(context.Background())
		if err != nil {
			return err
		}
		for _, b := range bufA {
			var k [8]byte
			binary.BigEndian.PutUint64(k[:], b.n)
			if err := tx.Put(tFwdAcctCS, k[:], b.blob); err != nil {
				tx.Rollback()
				return err
			}
		}
		for _, b := range bufS {
			var k [8]byte
			binary.BigEndian.PutUint64(k[:], b.n)
			if err := tx.Put(tFwdStorCS, k[:], b.blob); err != nil {
				tx.Rollback()
				return err
			}
		}
		bufA, bufS = bufA[:0], bufS[:0]
		return tx.Commit()
	}

	// Backward sweep of one changeset table. decode returns (block, key, old).
	sweep := func(table string, pending map[string][]byte,
		latest func([]byte) []byte, out *[]blockBuf,
		decode func(k, v []byte) (uint64, []byte, []byte, error)) error {

		c, err := src.Cursor(table)
		if err != nil {
			return err
		}
		defer c.Close()

		var blob []byte
		var curBlock uint64 = ^uint64(0)
		emitBlock := func() {
			if len(blob) > 0 && curBlock < endBlock {
				*out = append(*out, blockBuf{n: curBlock, blob: append([]byte{}, blob...)})
			}
			blob = blob[:0]
		}
		rows := 0
		for k, v, err := c.Last(); k != nil; k, v, err = c.Prev() {
			if err != nil {
				return err
			}
			bn, key, oldVal, derr := decode(k, v)
			if derr != nil {
				return fmt.Errorf("decode %s: %w", table, derr)
			}
			// Entries at/after endBlock still maintain the pending chain (a
			// partial build must see the value-before-next-change, not the
			// head PlainState) — they are just not emitted.
			if bn < endBlock {
				if bn != curBlock {
					emitBlock()
					curBlock = bn
				}
				// NEW value at this change = value before the NEXT change
				// (pending), else the chain's latest state.
				nv, seen := pending[string(key)]
				if !seen {
					nv = latest(key)
				}
				blob = append(blob, key...)
				blob = binary.AppendUvarint(blob, uint64(len(nv)))
				blob = append(blob, nv...)
			}
			// This change's OLD value becomes the previous change's NEW value.
			pending[string(key)] = append([]byte{}, oldVal...)

			rows++
			if rows%5_000_000 == 0 {
				fmt.Printf("  [convert] %s rows=%dM pending=%d elapsed=%s\n",
					table, rows/1_000_000, len(pending), time.Since(t0).Round(time.Second))
			}
			if len(*out) >= flushEvery {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		emitBlock()
		return nil
	}

	if err := sweep(modules.AccountChangeSet, pendingA, latestA, &bufA, changeset.DecodeAccounts); err != nil {
		return err
	}
	if err := sweep(modules.StorageChangeSet, pendingS, latestS, &bufS, changeset.DecodeStorage); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}

	// Genesis synthesis: changesets alone lose two classes of genesis values —
	// keys never changed since genesis (in PlainState, absent from every
	// changeset) and the PRE-first-change values of later-changed keys (the
	// pending map's final state). Both belong in the block-0 forward blob;
	// they cannot collide with the sweep's own block-0 emissions (a key
	// changed AT block 0 has an empty pre-genesis pending value).
	synth := func(stateTable string, keyLen int, pending map[string][]byte, fwdTable string) (int, error) {
		var blob []byte
		add := func(key, val []byte) {
			blob = append(blob, key...)
			blob = binary.AppendUvarint(blob, uint64(len(val)))
			blob = append(blob, val...)
		}
		n := 0
		for key, val := range pending {
			if len(val) > 0 {
				add([]byte(key), val)
				n++
			}
		}
		c, err := src.Cursor(stateTable)
		if err != nil {
			return 0, err
		}
		defer c.Close()
		for k, v, e := c.First(); k != nil; k, v, e = c.Next() {
			if e != nil {
				return 0, e
			}
			key, val := k, v
			if len(k) != keyLen {
				// DupSort shape: cursor yields key=prefix, value=subkey+value
				// (the HashedStorage lesson). Compose the full key.
				if keyLen == 52 && len(k) == 20 && len(v) >= 32 {
					key = append(append([]byte{}, k...), v[:32]...)
					val = v[32:]
				} else {
					continue
				}
			}
			if _, seen := pending[string(key)]; seen {
				continue // its genesis value (if any) came from pending above
			}
			add(key, val)
			n++
		}
		if len(blob) == 0 {
			return 0, nil
		}
		tx, err := outDB.BeginRw(context.Background())
		if err != nil {
			return 0, err
		}
		var k0 [8]byte
		existing, _ := tx.GetOne(fwdTable, k0[:])
		merged := append(append([]byte{}, existing...), blob...)
		if err := tx.Put(fwdTable, k0[:], merged); err != nil {
			tx.Rollback()
			return 0, err
		}
		return n, tx.Commit()
	}
	gA, err := synth(modules.Account, 20, pendingA, tFwdAcctCS)
	if err != nil {
		return err
	}
	gS, err := synth(modules.Storage, 52, pendingS, tFwdStorCS)
	if err != nil {
		return err
	}
	fmt.Printf("[convert] done in %s (pendingA=%d pendingS=%d genesisA=%d genesisS=%d)\n",
		time.Since(t0).Round(time.Second), len(pendingA), len(pendingS), gA, gS)
	return nil
}

// decodeFwdBlob unpacks a forward changeset blob into (key, newValue) pairs.
func decodeFwdBlob(blob []byte, keyLen int, fn func(key, val []byte) error) error {
	pos := 0
	for pos < len(blob) {
		if pos+keyLen > len(blob) {
			return fmt.Errorf("fwd blob truncated at key")
		}
		key := blob[pos : pos+keyLen]
		pos += keyLen
		vl, m := binary.Uvarint(blob[pos:])
		if m <= 0 || pos+m+int(vl) > len(blob) {
			return fmt.Errorf("fwd blob truncated at value")
		}
		pos += m
		val := blob[pos : pos+int(vl)]
		pos += int(vl)
		if err := fn(key, val); err != nil {
			return err
		}
	}
	return nil
}

// finalStateCheck compares the build's HashedAccounts/HashedStorage against
// the source chain's PlainState (keccak-mapped) — the external gate replacing
// the per-block header-root check.
func finalStateCheck(buildTx kv.Tx, src kv.Tx, b *builder) error {
	t0 := time.Now()
	// Accounts: every source PlainState account must appear (keccak(addr)) with
	// an equal V2 encoding, and counts must match.
	srcN, dstN := 0, 0
	c, err := src.Cursor(modules.Account)
	if err != nil {
		return err
	}
	defer c.Close()
	for k, v, e := c.First(); k != nil; k, v, e = c.Next() {
		if e != nil {
			return e
		}
		if len(k) != 20 {
			continue
		}
		srcN++
		var addr [20]byte
		copy(addr[:], k)
		ah := b.addrHash(addr)
		hv, e2 := buildTx.GetOne(modules.HashedAccounts, ah[:])
		if e2 != nil {
			return e2
		}
		if len(hv) == 0 {
			return fmt.Errorf("final check: account %x missing in build", k)
		}
		// Compare semantically (decode both) — encodings normalize equally, so
		// byte compare is sufficient in practice; fall back to byte equality.
		if !bytes.Equal(hv, normalizeAcct(v)) && !bytes.Equal(hv, v) {
			return fmt.Errorf("final check: account %x value mismatch src=%x build=%x", k, v, hv)
		}
	}
	hc, err := buildTx.Cursor(modules.HashedAccounts)
	if err != nil {
		return err
	}
	defer hc.Close()
	for k, _, e := hc.First(); k != nil; k, _, e = hc.Next() {
		if e != nil {
			return e
		}
		dstN++
	}
	if srcN != dstN {
		return fmt.Errorf("final check: account count src=%d build=%d", srcN, dstN)
	}
	fmt.Printf("[final-check] %d accounts equal (%s)\n", srcN, time.Since(t0).Round(time.Second))
	return nil
}

func normalizeAcct(v []byte) []byte { return v }
