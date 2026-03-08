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

package snapsync

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TaskKind identifies the type of snap sync download task.
type TaskKind int

const (
	TaskAccount TaskKind = iota
	TaskStorage
	TaskCode
)

// maxTaskRetries is the maximum number of retries for a single task.
const maxTaskRetries = 5

// taskTimeout is the maximum time to wait for a task response.
const taskTimeout = 30 * time.Second

// RangeTask represents a single unit of work for the download manager.
// For account and storage tasks, it defines a key range to fetch.
// For code tasks, it holds a list of code hashes to retrieve.
type RangeTask struct {
	Kind    TaskKind
	StartKey []byte // inclusive start (nil = beginning of table)
	EndKey   []byte // exclusive end (nil = end of table)
	Account  []byte // for TaskStorage: the account address

	// Code-specific: hashes to fetch (only used for TaskCode)
	CodeHashes [][]byte

	// Assignment tracking
	Assigned   peer.ID
	AssignedAt time.Time
	Retries    int
}

// IsAssigned returns true if the task is currently assigned to a peer.
func (t *RangeTask) IsAssigned() bool {
	return t.Assigned != ""
}

// IsTimedOut returns true if the task has exceeded the timeout.
func (t *RangeTask) IsTimedOut() bool {
	return t.IsAssigned() && time.Since(t.AssignedAt) > taskTimeout
}

// CanRetry returns true if the task has not exceeded the maximum retries.
func (t *RangeTask) CanRetry() bool {
	return t.Retries < maxTaskRetries
}

// Reset clears the assignment so the task can be reassigned.
func (t *RangeTask) Reset() {
	t.Assigned = ""
	t.AssignedAt = time.Time{}
}
