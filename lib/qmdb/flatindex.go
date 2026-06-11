// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// flatIndex — open-addressing keyHash→slot index over flat SoA arrays
// (SwissTable-lite). The Go map's pointer-chasing buckets were ~2/3 of the
// single-thread Set cost at million-key scale; here a probe touches a dense
// 1-byte control array (fingerprint + state), and only on a fingerprint hit
// compares the 32-byte key in a parallel flat array. keyHash is already a
// uniform Blake3 digest, so H1/H2 are taken directly from its bytes — no
// re-hashing.

package qmdb

import "encoding/binary"

const (
	fiEmpty = 0x00 // never used
	fiTomb  = 0x01 // deleted
	// occupied slots store 0x80 | fingerprint(7 bits)
)

type flatIndex struct {
	ctrl  []byte
	keys  []Hash
	slots []uint64
	mask  uint64
	live  int
	used  int // occupied + tombstones (grow/rehash trigger)
}

func newFlatIndex() *flatIndex {
	const initCap = 1 << 10
	return &flatIndex{
		ctrl:  make([]byte, initCap),
		keys:  make([]Hash, initCap),
		slots: make([]uint64, initCap),
		mask:  initCap - 1,
	}
}

func fiH1(k Hash) uint64 { return binary.LittleEndian.Uint64(k[0:8]) }
func fiFP(k Hash) byte   { return 0x80 | (k[8] & 0x7f) }

func (f *flatIndex) Get(k Hash) (uint64, bool) {
	fp := fiFP(k)
	i := fiH1(k) & f.mask
	for {
		c := f.ctrl[i]
		if c == fiEmpty {
			return 0, false
		}
		if c == fp && f.keys[i] == k {
			return f.slots[i], true
		}
		i = (i + 1) & f.mask
	}
}

func (f *flatIndex) Put(k Hash, slot uint64) {
	if uint64(f.used+1)*16 > (f.mask+1)*13 { // load factor 13/16
		f.rehash()
	}
	fp := fiFP(k)
	i := fiH1(k) & f.mask
	firstTomb := uint64(^uint64(0))
	for {
		c := f.ctrl[i]
		if c == fiEmpty {
			if firstTomb != ^uint64(0) {
				i = firstTomb // reuse the first tombstone on the probe path
			} else {
				f.used++
			}
			f.ctrl[i] = fp
			f.keys[i] = k
			f.slots[i] = slot
			f.live++
			return
		}
		if c == fiTomb {
			if firstTomb == ^uint64(0) {
				firstTomb = i
			}
		} else if c == fp && f.keys[i] == k {
			f.slots[i] = slot // overwrite
			return
		}
		i = (i + 1) & f.mask
	}
}

func (f *flatIndex) Delete(k Hash) {
	fp := fiFP(k)
	i := fiH1(k) & f.mask
	for {
		c := f.ctrl[i]
		if c == fiEmpty {
			return
		}
		if c == fp && f.keys[i] == k {
			f.ctrl[i] = fiTomb
			f.live--
			return
		}
		i = (i + 1) & f.mask
	}
}

func (f *flatIndex) Len() int { return f.live }

// rehash doubles capacity (or rebuilds in place size when tombstones dominate)
// and reinserts the live set.
func (f *flatIndex) rehash() {
	newCap := (f.mask + 1) * 2
	if f.live*3 < f.used*2 { // ≥1/3 tombstones: same size is enough
		newCap = f.mask + 1
	}
	oldCtrl, oldKeys, oldSlots := f.ctrl, f.keys, f.slots
	f.ctrl = make([]byte, newCap)
	f.keys = make([]Hash, newCap)
	f.slots = make([]uint64, newCap)
	f.mask = newCap - 1
	f.live, f.used = 0, 0
	for i := range oldCtrl {
		if oldCtrl[i] >= 0x80 {
			f.Put(oldKeys[i], oldSlots[i])
		}
	}
}
