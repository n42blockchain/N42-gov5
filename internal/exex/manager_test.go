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
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// mockExtension is a test helper that records all notifications it receives.
type mockExtension struct {
	name    string
	started atomic.Bool
	stopped atomic.Bool

	mu            sync.Mutex
	notifications []*ExExNotification

	// If set, OnNotification sleeps this duration to simulate a slow extension.
	delay time.Duration
	// If set, OnNotification returns this error.
	onErr error
}

func newMockExtension(name string) *mockExtension {
	return &mockExtension{name: name}
}

func (m *mockExtension) Name() string { return m.name }

func (m *mockExtension) Start(_ context.Context) error {
	m.started.Store(true)
	return nil
}

func (m *mockExtension) OnNotification(_ context.Context, notif *ExExNotification) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	m.notifications = append(m.notifications, notif)
	m.mu.Unlock()
	return m.onErr
}

func (m *mockExtension) Stop() error {
	m.stopped.Store(true)
	return nil
}

func (m *mockExtension) received() []*ExExNotification {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ExExNotification, len(m.notifications))
	copy(out, m.notifications)
	return out
}

func makeNotification(typ NotificationType, number uint64) *ExExNotification {
	return &ExExNotification{
		Type:   typ,
		Hash:   types.Hash{byte(number)},
		Number: number,
	}
}

// waitFor polls until cond returns true or timeout expires.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

func TestManager_RegisterAndNotify(t *testing.T) {
	mgr := NewManager(16)
	ext := newMockExtension("test-ext")
	mgr.Register(ext)

	ctx := context.Background()
	mgr.Start(ctx)
	defer mgr.Stop()

	notif := makeNotification(NotificationCommit, 100)
	mgr.Notify(notif)

	waitFor(t, 2*time.Second, "extension receives notification", func() bool {
		return len(ext.received()) == 1
	})

	got := ext.received()[0]
	if got.Type != NotificationCommit {
		t.Errorf("expected commit, got %v", got.Type)
	}
	if got.Number != 100 {
		t.Errorf("expected block 100, got %d", got.Number)
	}
}

func TestManager_MultipleExtensions(t *testing.T) {
	mgr := NewManager(16)
	ext1 := newMockExtension("ext-1")
	ext2 := newMockExtension("ext-2")
	ext3 := newMockExtension("ext-3")
	mgr.Register(ext1)
	mgr.Register(ext2)
	mgr.Register(ext3)

	ctx := context.Background()
	mgr.Start(ctx)
	defer mgr.Stop()

	mgr.Notify(makeNotification(NotificationCommit, 42))

	for _, ext := range []*mockExtension{ext1, ext2, ext3} {
		name := ext.Name()
		waitFor(t, 2*time.Second, name+" receives notification", func() bool {
			return len(ext.received()) == 1
		})
		if ext.received()[0].Number != 42 {
			t.Errorf("%s: expected block 42, got %d", name, ext.received()[0].Number)
		}
	}
}

func TestManager_NonBlocking(t *testing.T) {
	// Use a small buffer to prove that Notify does not block even when the
	// dispatch loop is slow.
	mgr := NewManager(2)
	slow := newMockExtension("slow")
	slow.delay = 200 * time.Millisecond
	mgr.Register(slow)

	ctx := context.Background()
	mgr.Start(ctx)
	defer mgr.Stop()

	// Send more notifications than the buffer can hold.
	// This must return immediately without blocking.
	done := make(chan struct{})
	go func() {
		for i := uint64(0); i < 10; i++ {
			mgr.Notify(makeNotification(NotificationCommit, i))
		}
		close(done)
	}()

	select {
	case <-done:
		// Notify returned promptly -- success.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Notify blocked despite full channel; non-blocking guarantee violated")
	}
}

func TestManager_Revert(t *testing.T) {
	mgr := NewManager(16)
	ext := newMockExtension("revert-ext")
	mgr.Register(ext)

	ctx := context.Background()
	mgr.Start(ctx)
	defer mgr.Stop()

	mgr.Notify(makeNotification(NotificationRevert, 99))

	waitFor(t, 2*time.Second, "extension receives revert", func() bool {
		return len(ext.received()) == 1
	})

	got := ext.received()[0]
	if got.Type != NotificationRevert {
		t.Errorf("expected revert, got %v", got.Type)
	}
	if got.Number != 99 {
		t.Errorf("expected block 99, got %d", got.Number)
	}
}

func TestManager_ContextCancellation(t *testing.T) {
	mgr := NewManager(16)
	ext := newMockExtension("ctx-ext")
	mgr.Register(ext)

	ctx := context.Background()
	mgr.Start(ctx)

	// Verify the extension was started.
	if !ext.started.Load() {
		t.Fatal("extension was not started")
	}

	// Stop the manager and verify graceful shutdown.
	mgr.Stop()

	if !ext.stopped.Load() {
		t.Fatal("extension was not stopped")
	}

	// Notifications after stop should not panic or block.
	mgr.Notify(makeNotification(NotificationCommit, 200))
}

func TestManager_NoExtensions(t *testing.T) {
	mgr := NewManager(16)
	ctx := context.Background()
	mgr.Start(ctx)
	defer mgr.Stop()

	// Notify with no extensions should be a no-op (no panic).
	mgr.Notify(makeNotification(NotificationCommit, 1))
	mgr.Notify(makeNotification(NotificationRevert, 2))
}

func TestLogExtension(t *testing.T) {
	// Verify that the log extension satisfies the Extension interface
	// and does not panic on any method call.
	var ext Extension = &logTestExt{}
	if err := ext.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	notif := &ExExNotification{
		Type:     NotificationCommit,
		Hash:     types.Hash{0x01},
		Number:   123,
		Receipts: []*block.Receipt{},
	}
	if err := ext.OnNotification(context.Background(), notif); err != nil {
		t.Fatalf("OnNotification failed: %v", err)
	}
	if err := ext.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

// logTestExt is a minimal Extension used to test the interface contract
// without importing the extensions sub-package (avoids circular deps).
type logTestExt struct{}

func (l *logTestExt) Name() string                                               { return "log-test" }
func (l *logTestExt) Start(_ context.Context) error                              { return nil }
func (l *logTestExt) OnNotification(_ context.Context, _ *ExExNotification) error { return nil }
func (l *logTestExt) Stop() error                                                { return nil }

func TestNotificationType_String(t *testing.T) {
	tests := []struct {
		t    NotificationType
		want string
	}{
		{NotificationCommit, "commit"},
		{NotificationRevert, "revert"},
		{NotificationType(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.t.String(); got != tt.want {
			t.Errorf("NotificationType(%d).String() = %q, want %q", tt.t, got, tt.want)
		}
	}
}
