// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Genesis alloc repair for a profile-1 replay (owner directive 2026-11-28).
// Every block executes under profile-1 / Amsterdam rules; only the genesis
// alloc may change so that most historic transactions still succeed. A
// repair pass replays with --p1-repair-out and, when a transaction would
// fail for a reason a genesis balance removes, injects the fix into the
// state right before the transaction and records it:
//
//   - precreate: a top-level value transfer into an empty account whose gas
//     limit cannot pay the EIP-8037 new-account state gas (a 21,000-gas
//     transfer to a fresh address) halts with status 0 and moves no value.
//     One wei in the recipient makes it non-empty (EIP-161), so no state gas
//     is charged (state_transition_amsterdam.go chargeCallRecipient).
//   - topup: a sender that cannot pay gas * price + value is credited the
//     exact shortfall (want - have).
//
// The recorded amounts summed per address are the alloc delta that
// replay-p1-genesis --extra-alloc adds to the genesis. Balances are additive,
// so the genesis credit leaves every account with the balance the injection
// gave it at the point of use; a transaction whose logic reads a balance
// earlier may still diverge, and the next repair pass on the new genesis
// records what is left (iterate to a fixed point).

package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// P1AllocDeltaVersion is the format version of the alloc delta file.
const P1AllocDeltaVersion = 1

// P1DeltaEntry is the genesis credit of one address.
type P1DeltaEntry struct {
	Address    types.Address
	Wei        uint256.Int
	Precreate  bool   // credited one wei as a fresh transfer recipient
	Topups     uint64 // shortfall top-ups as a sender
	FirstBlock uint64 // target block of the first injection
}

// P1AllocDelta is the accumulated alloc delta of a repair pass.
type P1AllocDelta struct {
	Entries map[types.Address]*P1DeltaEntry
}

// NewP1AllocDelta returns an empty delta.
func NewP1AllocDelta() *P1AllocDelta {
	return &P1AllocDelta{Entries: make(map[types.Address]*P1DeltaEntry)}
}

func (d *P1AllocDelta) entry(addr types.Address, number uint64) *P1DeltaEntry {
	e := d.Entries[addr]
	if e == nil {
		e = &P1DeltaEntry{Address: addr, FirstBlock: number}
		d.Entries[addr] = e
	}
	return e
}

// AddPrecreate records a one-wei credit to a fresh transfer recipient.
func (d *P1AllocDelta) AddPrecreate(addr types.Address, number uint64) {
	e := d.entry(addr, number)
	e.Precreate = true
	e.Wei.AddUint64(&e.Wei, 1)
}

// AddTopup records a shortfall credit to a sender.
func (d *P1AllocDelta) AddTopup(addr types.Address, amount *uint256.Int, number uint64) {
	e := d.entry(addr, number)
	e.Topups++
	e.Wei.Add(&e.Wei, amount)
}

// Total returns the summed credit.
func (d *P1AllocDelta) Total() *uint256.Int {
	t := new(uint256.Int)
	for _, e := range d.Entries {
		t.Add(t, &e.Wei)
	}
	return t
}

type p1DeltaEntryJSON struct {
	Address    types.Address `json:"address"`
	Wei        string        `json:"wei"`
	Precreate  bool          `json:"precreate,omitempty"`
	Topups     uint64        `json:"topups,omitempty"`
	FirstBlock uint64        `json:"firstBlock"`
}

type p1DeltaFileJSON struct {
	Version    int                `json:"version"`
	Addresses  int                `json:"addresses"`
	Precreates int                `json:"precreates"`
	TotalWei   string             `json:"totalWei"`
	Entries    []p1DeltaEntryJSON `json:"entries"`
}

// MarshalJSON writes the delta sorted by address.
func (d *P1AllocDelta) MarshalJSON() ([]byte, error) {
	f := p1DeltaFileJSON{Version: P1AllocDeltaVersion, Addresses: len(d.Entries), TotalWei: d.Total().Dec()}
	f.Entries = make([]p1DeltaEntryJSON, 0, len(d.Entries))
	for _, e := range d.Entries {
		if e.Precreate {
			f.Precreates++
		}
		f.Entries = append(f.Entries, p1DeltaEntryJSON{Address: e.Address, Wei: e.Wei.Dec(), Precreate: e.Precreate, Topups: e.Topups, FirstBlock: e.FirstBlock})
	}
	sort.Slice(f.Entries, func(i, j int) bool {
		return string(f.Entries[i].Address[:]) < string(f.Entries[j].Address[:])
	})
	return json.Marshal(f)
}

// UnmarshalJSON reads a delta written by MarshalJSON.
func (d *P1AllocDelta) UnmarshalJSON(b []byte) error {
	var f p1DeltaFileJSON
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	if f.Version != P1AllocDeltaVersion {
		return fmt.Errorf("alloc delta version %d, want %d", f.Version, P1AllocDeltaVersion)
	}
	d.Entries = make(map[types.Address]*P1DeltaEntry, len(f.Entries))
	for _, je := range f.Entries {
		if _, dup := d.Entries[je.Address]; dup {
			return fmt.Errorf("alloc delta: duplicate address %s", je.Address.Hex())
		}
		e := &P1DeltaEntry{Address: je.Address, Precreate: je.Precreate, Topups: je.Topups, FirstBlock: je.FirstBlock}
		if err := e.Wei.SetFromDecimal(je.Wei); err != nil {
			return fmt.Errorf("alloc delta %s: wei %q: %w", je.Address.Hex(), je.Wei, err)
		}
		d.Entries[je.Address] = e
	}
	return nil
}

// ReadP1AllocDelta reads an alloc delta file.
func ReadP1AllocDelta(path string) (*P1AllocDelta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d := NewP1AllocDelta()
	if err := json.Unmarshal(b, d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// WriteP1AllocDelta writes d to path atomically (temporary file + rename).
func WriteP1AllocDelta(path string, d *P1AllocDelta) error {
	b, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// p1RepairOpen prepares the repair accumulator once the resume point of the
// target is known: a fresh target starts an empty delta and refuses to
// overwrite an existing file; a resumed target continues the file.
func (e *EngineV2) p1RepairOpen(resumeBlock uint64) error {
	path := e.cfg.Profile1RepairOut
	if path == "" {
		return nil
	}
	if !e.profile1() {
		return errors.New("--p1-repair-out needs --p1-genesis")
	}
	_, statErr := os.Stat(path)
	exists := statErr == nil
	switch {
	case resumeBlock == 0 && exists:
		return fmt.Errorf("repair: %s exists but the target is fresh; remove it or choose another path", path)
	case resumeBlock > 0 && exists:
		d, err := ReadP1AllocDelta(path)
		if err != nil {
			return fmt.Errorf("repair: resume: %w", err)
		}
		e.p1Repair = d
	case resumeBlock > 0:
		return fmt.Errorf("repair: the target resumes at %d but %s does not exist", resumeBlock, path)
	default:
		e.p1Repair = NewP1AllocDelta()
	}
	e.log.Info("profile-1 genesis repair enabled", "out", path, "entries", len(e.p1Repair.Entries))
	return nil
}

// p1RepairFlush writes the accumulated delta; called after each committed
// batch so a resumed pass continues from what the target holds.
func (e *EngineV2) p1RepairFlush() error {
	if e.p1Repair == nil {
		return nil
	}
	if err := WriteP1AllocDelta(e.cfg.Profile1RepairOut, e.p1Repair); err != nil {
		return fmt.Errorf("repair: write %s: %w", e.cfg.Profile1RepairOut, err)
	}
	return nil
}

// p1RepairPrecreate gives the recipient of a transfer that would halt on
// the new-account state gas one wei before the transaction runs.
func (e *EngineV2) p1RepairPrecreate(ibs *state.IntraBlockState, to types.Address, number uint64) {
	ibs.AddBalance(to, uint256.NewInt(1))
	e.p1Repair.AddPrecreate(to, number)
	e.stats.P1Diag.Add("repair_precreate", 1)
}
