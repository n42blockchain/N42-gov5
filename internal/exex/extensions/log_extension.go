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
// log_extension.go — minimal example ExEx extension.
//
// LogExtension implements exex.Extension by logging every commit and
// revert notification it receives. It serves as both a reference
// implementation for new extensions and a useful development-time
// debug sink so operators can confirm that the execution extension
// pipeline is wired up correctly without installing a full indexer.

package extensions

import (
	"context"

	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/log"
)

// LogExtension is a built-in example extension that logs every block
// commit and revert event. It serves as both a reference implementation
// and a useful debugging tool during development.
type LogExtension struct{}

var _ exex.Extension = (*LogExtension)(nil)

// Name returns the extension identifier.
func (l *LogExtension) Name() string {
	return "log"
}

// Start is a no-op for the log extension.
func (l *LogExtension) Start(_ context.Context) error {
	log.Info("ExEx log extension started")
	return nil
}

// OnNotification logs the block event details.
func (l *LogExtension) OnNotification(_ context.Context, notif *exex.ExExNotification) error {
	txCount := 0
	if notif.Block != nil {
		txCount = len(notif.Block.Transactions())
	}
	receiptCount := len(notif.Receipts)

	log.Debug("ExEx event",
		"type", notif.Type.String(),
		"number", notif.Number,
		"hash", notif.Hash.String(),
		"txs", txCount,
		"receipts", receiptCount,
	)
	return nil
}

// Stop is a no-op for the log extension.
func (l *LogExtension) Stop() error {
	log.Info("ExEx log extension stopped")
	return nil
}
