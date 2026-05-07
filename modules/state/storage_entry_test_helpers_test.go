package state

// storageEntryFromBytes builds an entry from a byte slice, padding /
// truncating to fit the inline 32-byte slab. Test-only — production
// writes go through WriteAccountStorage's WriteToSlice path which
// avoids the per-write copy.
func storageEntryFromBytes(b []byte) storageEntry {
	var e storageEntry
	n := len(b)
	if n > 32 {
		n = 32
	}
	if n > 0 {
		copy(e.value[32-n:], b[:n])
	}
	e.valLen = uint8(n)
	return e
}
