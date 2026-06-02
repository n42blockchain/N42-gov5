// Package bodyf2 is the production F2 body format: a trust-history (no-signature)
// columnar body encoding that keeps the full ledger (from/to/value/nonce/gas/
// input/accessList/withdrawals) but drops R/S/V, cutting on-disk size ~45% (see
// docs/ethel/body-compression-design.md §4/§5). Senders and to-addresses are
// stored as small IDs into a store-wide AddrDict sidecar instead of 20-byte
// addresses; From is read from the ID (no ecrecover) and the canonical tx hash
// is not reproducible (served via the MPHF index / F1.5, out of this package).
//
// This file: AddrDict — the global address↔ID dictionary.
package bodyf2

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/types"
)

// dictMagic identifies the on-disk dictionary sidecar.
var dictMagic = [4]byte{'A', 'D', 'C', '1'}

// AddrDict maps 20-byte addresses to dense uint32 IDs (first-seen order) and
// back. The reverse (ID→addr) is the slice index. Not safe for concurrent
// Intern; build single-threaded, then load read-only for decode.
type AddrDict struct {
	ids  map[types.Address]uint32
	list []types.Address
}

// NewAddrDict returns an empty dictionary.
func NewAddrDict() *AddrDict {
	return &AddrDict{ids: make(map[types.Address]uint32)}
}

// Intern returns the ID for addr, assigning a new one on first sight.
func (d *AddrDict) Intern(a types.Address) uint32 {
	if id, ok := d.ids[a]; ok {
		return id
	}
	id := uint32(len(d.list))
	d.ids[a] = id
	d.list = append(d.list, a)
	return id
}

// ID returns the existing ID for addr (false if absent — encode-time guard).
func (d *AddrDict) ID(a types.Address) (uint32, bool) {
	id, ok := d.ids[a]
	return id, ok
}

// Addr returns the address for an ID (false if out of range — decode-time guard).
func (d *AddrDict) Addr(id uint32) (types.Address, bool) {
	if id >= uint32(len(d.list)) {
		return types.Address{}, false
	}
	return d.list[id], true
}

// Len returns the number of unique addresses.
func (d *AddrDict) Len() int { return len(d.list) }

// Save writes the dictionary: magic(4) + count(u32 LE) + count×20B addresses
// (ID = position). ~20B/unique addr; a store-wide sidecar amortized over all tx.
func (d *AddrDict) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.Write(dictMagic[:]); err != nil {
		return err
	}
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(d.list)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	for i := range d.list {
		if _, err := w.Write(d.list[i][:]); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

// LoadAddrDict reads a dictionary written by Save.
func LoadAddrDict(path string) (*AddrDict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 8 || data[0] != dictMagic[0] || data[1] != dictMagic[1] || data[2] != dictMagic[2] || data[3] != dictMagic[3] {
		return nil, fmt.Errorf("bodyf2: bad addr-dict magic")
	}
	count := binary.LittleEndian.Uint32(data[4:8])
	if len(data) < 8+int(count)*20 {
		return nil, fmt.Errorf("bodyf2: addr-dict truncated: want %d addrs, have %d bytes", count, len(data)-8)
	}
	d := &AddrDict{ids: make(map[types.Address]uint32, count), list: make([]types.Address, count)}
	pos := 8
	for i := uint32(0); i < count; i++ {
		copy(d.list[i][:], data[pos:pos+20])
		d.ids[d.list[i]] = i
		pos += 20
	}
	return d, nil
}
