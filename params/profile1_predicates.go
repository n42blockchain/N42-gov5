// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Profile-1 routing of the HotStuff activation switches: under
// config.n42Profile = 1 each of them holds from view 0 with no field.

package params

// HotStuffTwoPhaseVoteGate reports whether Round-1 votes fire on static
// validity and the CommitVote waits for the local import (A13): always on
// under Profile 1, else the legacy hotstuff.twoPhaseVoteGate flag.
func (c *ChainConfig) HotStuffTwoPhaseVoteGate() bool {
	if c.IsProfile1() {
		return true
	}
	return c != nil && c.HotStuff != nil && c.HotStuff.TwoPhaseVoteGate
}

// HotStuffInteropV4 reports whether consensus messages are signed under the
// chain-bound H2-v4 domains (A14): always under Profile 1, else the legacy
// hotstuff.interopV4 flag.
func (c *ChainConfig) HotStuffInteropV4() bool {
	if c.IsProfile1() {
		return true
	}
	return c != nil && c.HotStuff != nil && c.HotStuff.InteropV4
}

// HotStuffCommitteePoolEnabled reports whether the BLS committee pool runs
// (A19): under Profile 1 whenever committeePool is present (it is mandatory
// there and has no enabled flag), else the legacy committeePool.enabled.
func (c *ChainConfig) HotStuffCommitteePoolEnabled() bool {
	if c == nil || c.HotStuff == nil || c.HotStuff.CommitteePool == nil {
		return false
	}
	if c.IsProfile1() {
		return true
	}
	return c.HotStuff.CommitteePool.Enabled
}

// HotStuffRotateOnTimeout reports whether rotate-on-timeout is configured by
// the chain and the first TC view it applies to (A22, SYNC §10.2): from view
// 0 under Profile 1, else the legacy hotstuff.rotateOnTimeoutView.
func (c *ChainConfig) HotStuffRotateOnTimeout() (on bool, fromView uint64) {
	if c.IsProfile1() {
		return true, 0
	}
	if c != nil && c.HotStuff != nil && c.HotStuff.RotateOnTimeoutView != nil {
		return true, *c.HotStuff.RotateOnTimeoutView
	}
	return false, 0
}

// HotStuffTimeoutsMs returns the pacemaker's base and maximum view timeouts
// in milliseconds (A16): baseTimeoutMs/maxTimeoutMs under Profile 1, else
// the legacy baseTimeout/maxTimeout.
func (c *ChainConfig) HotStuffTimeoutsMs() (base, max uint64) {
	if c == nil || c.HotStuff == nil {
		return 0, 0
	}
	if c.IsProfile1() {
		return c.HotStuff.BaseTimeoutMs, c.HotStuff.MaxTimeoutMs
	}
	return c.HotStuff.BaseTimeout, c.HotStuff.MaxTimeout
}
