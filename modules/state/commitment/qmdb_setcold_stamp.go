// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import "time"

// SetColdStamp is the split of the most recent SetCold (r97a stamps): the
// wait for the tree's lock and the install, and the writer holding the lock
// when the call began (only when it waited over lookupWaitNote).
type SetColdStamp struct {
	Wait, Work time.Duration
	Holder     uint32 // QMDBHolderName; holderNone when the lock was free
}

// LastSetCold returns the stamp of the most recent SetCold (zero before the
// first).
func (r *QMDBRootComputer) LastSetCold() SetColdStamp {
	if s := r.lastSetCold.Load(); s != nil {
		return *s
	}
	return SetColdStamp{}
}
