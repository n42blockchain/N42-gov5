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

package exex

import (
	"context"
	"sync"

	"github.com/n42blockchain/N42/log"
)

const (
	// defaultBufSize is the default notification channel buffer size.
	defaultBufSize = 64
)

// Extension is the interface that execution extension plugins must implement.
// Each extension runs in its own goroutine and receives block notifications
// after execution completes in the core blockchain.
type Extension interface {
	// Name returns the extension identifier, used for logging.
	Name() string
	// Start is called once when the node starts. It receives a context that
	// is cancelled during shutdown.
	Start(ctx context.Context) error
	// OnNotification is called for each post-execution event. Implementations
	// must be safe for concurrent use if multiple managers share the extension.
	OnNotification(ctx context.Context, notif *ExExNotification) error
	// Stop is called during graceful node shutdown.
	Stop() error
}

// Manager distributes post-execution block notifications to all registered
// extensions. It decouples the core block processing pipeline from extension
// logic via a buffered channel so that slow extensions never block block
// production or import.
type Manager struct {
	extensions []Extension
	notifyCh   chan ExExNotification
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// NewManager creates a new ExEx manager with the given notification buffer size.
// A bufSize of 0 uses the default (64).
func NewManager(bufSize int) *Manager {
	if bufSize <= 0 {
		bufSize = defaultBufSize
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		notifyCh: make(chan ExExNotification, bufSize),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Register adds an extension to the manager. Must be called before Start.
func (m *Manager) Register(ext Extension) {
	m.extensions = append(m.extensions, ext)
	log.Info("ExEx extension registered", "name", ext.Name())
}

// Start initializes all registered extensions and launches the dispatch loop.
// The provided context is used as the parent for extension lifecycles.
func (m *Manager) Start(ctx context.Context) {
	if len(m.extensions) == 0 {
		log.Debug("ExEx manager started with no extensions")
		return
	}

	// Start each extension.
	for _, ext := range m.extensions {
		if err := ext.Start(ctx); err != nil {
			log.Error("ExEx extension failed to start", "name", ext.Name(), "err", err)
		}
	}

	// Launch the dispatch goroutine.
	m.wg.Add(1)
	go m.dispatchLoop()

	log.Info("ExEx manager started", "extensions", len(m.extensions))
}

// Notify sends a notification to all extensions via the buffered channel.
// If the channel is full the notification is dropped to guarantee that the
// core block pipeline is never blocked by slow extensions.
func (m *Manager) Notify(notif *ExExNotification) {
	if len(m.extensions) == 0 {
		return
	}
	select {
	case m.notifyCh <- *notif:
	default:
		log.Warn("ExEx notification dropped, channel full",
			"type", notif.Type.String(),
			"block", notif.Number,
		)
	}
}

// Stop gracefully shuts down the dispatch loop and all registered extensions.
func (m *Manager) Stop() {
	m.cancel()
	m.wg.Wait()

	for _, ext := range m.extensions {
		if err := ext.Stop(); err != nil {
			log.Error("ExEx extension failed to stop", "name", ext.Name(), "err", err)
		}
	}

	log.Info("ExEx manager stopped")
}

// dispatchLoop reads notifications from the channel and fans them out to
// every registered extension sequentially. Each extension call has its own
// error handling so one failing extension does not prevent others from
// receiving the notification.
func (m *Manager) dispatchLoop() {
	defer m.wg.Done()

	for {
		select {
		case <-m.ctx.Done():
			return
		case notif, ok := <-m.notifyCh:
			if !ok {
				return
			}
			for _, ext := range m.extensions {
				if err := ext.OnNotification(m.ctx, &notif); err != nil {
					log.Error("ExEx extension notification failed",
						"name", ext.Name(),
						"type", notif.Type.String(),
						"block", notif.Number,
						"err", err,
					)
				}
			}
		}
	}
}
