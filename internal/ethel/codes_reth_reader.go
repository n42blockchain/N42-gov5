// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// RethCodesReader — a state.CodeSource that resolves H0 contract bytecode
// DIRECTLY from a reth MDBX (PlainAccountState[addr].bytecode_hash →
// Bytecodes[hash] → Compact decode), replacing the exported codes freezer for
// local snapshot-direct runs. The address-keyed freezer export from reth
// requires materializing one entry per address-with-code (~82M on 25.4M-block
// mainnet) — two point reads against the live reth DB serve the same lookups
// with zero derived data. Production distributions still ship the freezer;
// this is the local/dev source (--codes.reth-db).

package ethel

import (
	"context"
	"sync"

	"github.com/c2h5oh/datasize"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	rethTabPlainAccount = "PlainAccountState"
	rethTabBytecodes    = "Bytecodes"
)

type RethCodesReader struct {
	db kv.RoDB

	mu    sync.Mutex
	cache *lru.Cache[types.Address, []byte] // addr → decoded code (nil = no code)
}

// NewRethCodesReader opens the reth DB read-only (Accede: never creates).
func NewRethCodesReader(dbPath string) (*RethCodesReader, error) {
	db, err := mdbxkv.NewMDBX(log.New()).Path(dbPath).Label(kv.ChainDB).
		MapSize(4096 * datasize.GB).Accede().Readonly().
		WithTableCfg(func(defaults kv.TableCfg) kv.TableCfg {
			defaults[rethTabPlainAccount] = kv.TableCfgItem{}
			defaults[rethTabBytecodes] = kv.TableCfgItem{}
			return defaults
		}).
		Open(context.Background())
	if err != nil {
		return nil, err
	}
	c, _ := lru.New[types.Address, []byte](8192)
	return &RethCodesReader{db: db, cache: c}, nil
}

func (r *RethCodesReader) Close() { r.db.Close() }

// GetCode implements state.CodeSource: two point reads + Compact decodes.
// Absent account / no code / unreferenced hash all return (nil, nil) — the
// same "reads empty" contract as the freezer reader.
func (r *RethCodesReader) GetCode(addr types.Address) ([]byte, error) {
	r.mu.Lock()
	if code, ok := r.cache.Get(addr); ok {
		r.mu.Unlock()
		return code, nil
	}
	r.mu.Unlock()

	var code []byte
	err := r.db.View(context.Background(), func(tx kv.Tx) error {
		av, err := tx.GetOne(rethTabPlainAccount, addr[:])
		if err != nil || len(av) == 0 {
			return err
		}
		ch, ok := rethAccountCodeHash(av)
		if !ok {
			return nil
		}
		bv, err := tx.GetOne(rethTabBytecodes, ch[:])
		if err != nil || len(bv) == 0 {
			return err
		}
		code = rethCompactRawCode(bv)
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache.Add(addr, code)
	r.mu.Unlock()
	return code, nil
}

// rethAccountCodeHash extracts bytecode_hash from a reth Compact
// PlainAccountState value (flags u16 LE: nonceLen 0-3, balLen 4-9, hasHash
// bit 10 — the proven acct-probe layout). ok=false: no code / no parse.
func rethAccountCodeHash(v []byte) (ch types.Hash, ok bool) {
	if len(v) < 2 {
		return
	}
	flags := uint16(v[0]) | uint16(v[1])<<8
	nonceLen := int(flags & 0x0f)
	balLen := int((flags >> 4) & 0x3f)
	if (flags>>10)&1 != 1 {
		return
	}
	if len(v) != 2+nonceLen+balLen+32 {
		return
	}
	copy(ch[:], v[2+nonceLen+balLen:])
	return ch, true
}

// rethCompactRawCode decodes reth's Compact revm Bytecode value to the raw
// deployed code: [u32 BE padded_len][bytecode][variant u8][u64 BE original_len]
// [jumptable]; LegacyAnalyzed pads — original_len recovers the real length.
// Values without the trailer (e.g. EIP-7702 delegation designators) are the
// raw bytes verbatim.
func rethCompactRawCode(v []byte) []byte {
	if len(v) < 4 {
		return nil
	}
	pl := int(uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3]))
	if pl < 0 || 4+pl > len(v) {
		return nil
	}
	if len(v) >= 4+pl+9 {
		ol := int(uint64(v[4+pl+1])<<56 | uint64(v[4+pl+2])<<48 | uint64(v[4+pl+3])<<40 |
			uint64(v[4+pl+4])<<32 | uint64(v[4+pl+5])<<24 | uint64(v[4+pl+6])<<16 |
			uint64(v[4+pl+7])<<8 | uint64(v[4+pl+8]))
		if ol >= 0 && ol <= pl {
			out := make([]byte, ol)
			copy(out, v[4:4+ol])
			return out
		}
	}
	out := make([]byte, pl)
	copy(out, v[4:4+pl])
	return out
}
