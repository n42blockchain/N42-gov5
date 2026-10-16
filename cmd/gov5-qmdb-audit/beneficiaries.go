package main

import (
	"bytes"
	"errors"
	"sort"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

type emptyBeneficiary struct {
	Address  types.Address `json:"address"`
	Key      types.Hash    `json:"key_hash"`
	Slot     uint64        `json:"slot"`
	ValueHex string        `json:"value_hex"`
}

// This incident tool supports only a bounded chain with the exact, fixed
// genesis validator set. It does not authorize a new state-root rule.
func usedBeneficiaries(tx kv.Tx, applied *block.Header, genesis types.Hash, validators map[types.Address]bool) (map[qmdb.Hash]types.Address, error) {
	if applied == nil || applied.Number == nil || !applied.Number.IsUint64() || applied.Number.Uint64() > 4096 {
		return nil, errors.New("beneficiary ancestry outside bounded audit")
	}
	known := make(map[qmdb.Hash]types.Address)
	header := applied
	for height := applied.Number.Uint64(); height > 0; height-- {
		if !validators[header.Coinbase] {
			return nil, errors.New("header beneficiary outside fixed genesis validator set")
		}
		// A no-transaction block cannot explain this incident's zero-tip touch.
		if header.GasUsed > 0 {
			known[qmdb.Hash(commitment.AccountKeyHash(header.Coinbase))] = header.Coinbase
		}
		var err error
		header, err = checkedHeader(tx, header.ParentHash, height-1)
		if err != nil {
			return nil, err
		}
	}
	if header.Hash() != genesis {
		return nil, errors.New("beneficiary ancestry does not reach exact genesis")
	}
	return known, nil
}

// Match the existing QMDB reader's empty-account semantics only for beneficiary
// addresses independently bound to the stored chain. Unknown key types, empty
// storage, nonempty accounts and any existing PlainState row remain failures.
func auditEmptyBeneficiaries(tx kv.Tx, live map[qmdb.Hash]liveValue, known map[qmdb.Hash]types.Address) ([]emptyBeneficiary, error) {
	var out []emptyBeneficiary
	for key, entry := range live {
		address, ok := known[key]
		if !ok || key != qmdb.Hash(commitment.AccountKeyHash(address)) || !bytes.Equal(entry.value, []byte{0}) {
			return nil, unmatchedPlainState(live)
		}
		exists, err := tx.Has(modules.Account, address[:])
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New("retained empty beneficiary has a PlainState row")
		}
		out = append(out, emptyBeneficiary{Address: address, Key: types.Hash(key), Slot: entry.slot, ValueHex: "00"})
	}
	// Do not alter the caller's live set until every exception is checked.
	for key := range live {
		delete(live, key)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Key[:], out[j].Key[:]) < 0 })
	return out, nil
}
