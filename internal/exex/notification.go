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
// notification.go — post-execution event types delivered to extensions.
//
// NotificationType distinguishes NotificationCommit (a block was just
// committed to the canonical chain) from NotificationRevert (a block
// was reverted during a chain reorganisation). ExExNotification carries
// the block header, receipts and reorg metadata so extensions can
// update derived state incrementally without re-reading from MDBX.
// The String method produces a human-readable label for logging.

package exex

import (
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// NotificationType identifies the kind of post-execution event.
type NotificationType int

const (
	// NotificationCommit is emitted when a new block is committed to the canonical chain.
	NotificationCommit NotificationType = iota
	// NotificationRevert is emitted when a block is reverted during a chain reorganization.
	NotificationRevert
)

// String returns a human-readable label for the notification type.
func (t NotificationType) String() string {
	switch t {
	case NotificationCommit:
		return "commit"
	case NotificationRevert:
		return "revert"
	default:
		return "unknown"
	}
}

// ExExNotification represents a post-execution event delivered to extensions.
// It carries all relevant context about the block that was committed or reverted.
type ExExNotification struct {
	// Type indicates whether this is a commit or revert event.
	Type NotificationType
	// Block that was committed or reverted.
	Block block.IBlock
	// Receipts for the block. Nil for revert notifications.
	Receipts []*block.Receipt
	// Hash is the block hash, provided for convenience.
	Hash types.Hash
	// Number is the block number, provided for convenience.
	Number uint64
}
