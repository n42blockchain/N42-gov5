// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import "time"

// SetColdStamp is the split of the most recent SetCold or SetColdLean (r97a
// stamps): the wait for the tree's lock and the install, the writer holding
// the lock when the call began (only when it waited over lookupWaitNote, or
// a SetColdLean parked), and for a parked call when and by which exclusive
// section it was adopted.
type SetColdStamp struct {
	Wait, Work time.Duration
	Holder     uint32 // QMDBHolderName; holderNone when the lock was free
	Parked     bool
	AdoptLag   time.Duration // park -> adoption (parked only; 0 until adopted)
	AdoptBy    uint32        // the adopting section's holder code
}

// LastSetCold returns the stamp of the most recent SetCold or SetColdLean
// (zero before the first). A parked call's adoption fields fill in when a
// section adopts it.
func (r *QMDBRootComputer) LastSetCold() SetColdStamp {
	if s := r.lastSetCold.Load(); s != nil {
		return *s
	}
	return SetColdStamp{}
}
