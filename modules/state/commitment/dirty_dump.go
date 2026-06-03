// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

// DirtyDump is the gob shape consumed by offline trie debugging commands.
// It intentionally carries already-hashed account and storage keys because
// those commands replay changes against HashedAccount/HashedStorage tables.
type DirtyDump struct {
	Accounts []DirtyAccount
	Storage  []DirtyStorage
}

type DirtyAccount struct {
	AddrHash [32]byte
	Value    []byte
}

type DirtyStorage struct {
	AddrHash [32]byte
	Slots    []DirtySlot
}

type DirtySlot struct {
	SlotHash [32]byte
	Value    []byte
}
