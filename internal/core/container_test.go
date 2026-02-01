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

package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// mockService is a test implementation of Service
type mockService struct {
	name        string
	status      atomic.Int32
	startCalled atomic.Int32
	stopCalled  atomic.Int32
	startErr    error
	stopErr     error
	startDelay  time.Duration
	stopDelay   time.Duration
}

func newMockService(name string) *mockService {
	s := &mockService{name: name}
	s.status.Store(int32(StatusStopped))
	return s
}

func (s *mockService) Name() string {
	return s.name
}

func (s *mockService) Start(ctx context.Context) error {
	s.startCalled.Add(1)
	s.status.Store(int32(StatusStarting))

	if s.startDelay > 0 {
		select {
		case <-time.After(s.startDelay):
		case <-ctx.Done():
			s.status.Store(int32(StatusStopped))
			return ctx.Err()
		}
	}

	if s.startErr != nil {
		s.status.Store(int32(StatusStopped))
		return s.startErr
	}

	s.status.Store(int32(StatusRunning))
	return nil
}

func (s *mockService) Stop(ctx context.Context) error {
	s.stopCalled.Add(1)
	s.status.Store(int32(StatusStopping))

	if s.stopDelay > 0 {
		select {
		case <-time.After(s.stopDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if s.stopErr != nil {
		return s.stopErr
	}

	s.status.Store(int32(StatusStopped))
	return nil
}

func (s *mockService) Status() ServiceStatus {
	return ServiceStatus(s.status.Load())
}

func TestNewServiceContainer(t *testing.T) {
	c := NewServiceContainer()
	if c == nil {
		t.Fatal("NewServiceContainer returned nil")
	}
	if c.services == nil {
		t.Error("services map is nil")
	}
	if c.order == nil {
		t.Error("order slice is nil")
	}
	t.Log("✓ NewServiceContainer creates valid container")
}

func TestRegisterService(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	s2 := newMockService("service2")
	s3 := newMockService("service1") // duplicate name

	// Register first service
	if err := c.Register(s1); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Register second service
	if err := c.Register(s2); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Register duplicate should fail
	if err := c.Register(s3); err == nil {
		t.Error("Register should fail for duplicate name")
	}

	// Verify order
	if len(c.order) != 2 {
		t.Errorf("Expected 2 services, got %d", len(c.order))
	}
	if c.order[0] != "service1" || c.order[1] != "service2" {
		t.Errorf("Unexpected order: %v", c.order)
	}

	t.Log("✓ Register handles services correctly")
}

func TestGetService(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	_ = c.Register(s1)

	// Get existing service
	svc, ok := c.Get("service1")
	if !ok {
		t.Error("Get should find service1")
	}
	if svc != s1 {
		t.Error("Get returned wrong service")
	}

	// Get non-existing service
	_, ok = c.Get("nonexistent")
	if ok {
		t.Error("Get should not find nonexistent service")
	}

	t.Log("✓ Get retrieves services correctly")
}

func TestStartAllServices(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	s2 := newMockService("service2")
	s3 := newMockService("service3")

	_ = c.Register(s1)
	_ = c.Register(s2)
	_ = c.Register(s3)

	ctx := context.Background()
	if err := c.StartAll(ctx); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	// Verify all services started
	for _, s := range []*mockService{s1, s2, s3} {
		if s.startCalled.Load() != 1 {
			t.Errorf("%s: Start should be called once", s.name)
		}
		if s.Status() != StatusRunning {
			t.Errorf("%s: should be running", s.name)
		}
	}

	t.Log("✓ StartAll starts all services in order")
}

func TestStartAllWithError(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	s2 := newMockService("service2")
	s2.startErr = errors.New("start failed")
	s3 := newMockService("service3")

	_ = c.Register(s1)
	_ = c.Register(s2)
	_ = c.Register(s3)

	ctx := context.Background()
	err := c.StartAll(ctx)
	if err == nil {
		t.Fatal("StartAll should fail when service fails")
	}

	// s1 should have been stopped after s2 failed
	if s1.Status() != StatusStopped {
		t.Error("s1 should be stopped after rollback")
	}

	// s3 should never have been started
	if s3.startCalled.Load() != 0 {
		t.Error("s3 should not have been started")
	}

	t.Log("✓ StartAll rolls back on failure")
}

func TestStopAllServices(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	s2 := newMockService("service2")
	s3 := newMockService("service3")

	_ = c.Register(s1)
	_ = c.Register(s2)
	_ = c.Register(s3)

	ctx := context.Background()
	_ = c.StartAll(ctx)

	if err := c.StopAll(ctx); err != nil {
		t.Fatalf("StopAll failed: %v", err)
	}

	// Verify all services stopped
	for _, s := range []*mockService{s1, s2, s3} {
		if s.stopCalled.Load() != 1 {
			t.Errorf("%s: Stop should be called once", s.name)
		}
		if s.Status() != StatusStopped {
			t.Errorf("%s: should be stopped", s.name)
		}
	}

	t.Log("✓ StopAll stops all services in reverse order")
}

func TestStopAllWithError(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	s2 := newMockService("service2")
	s2.stopErr = errors.New("stop failed")
	s3 := newMockService("service3")

	_ = c.Register(s1)
	_ = c.Register(s2)
	_ = c.Register(s3)

	ctx := context.Background()
	_ = c.StartAll(ctx)

	err := c.StopAll(ctx)
	if err == nil {
		t.Fatal("StopAll should fail when service fails")
	}

	// All services should still have stop called
	if s1.stopCalled.Load() != 1 {
		t.Error("s1.Stop should be called")
	}
	if s2.stopCalled.Load() != 1 {
		t.Error("s2.Stop should be called")
	}
	if s3.stopCalled.Load() != 1 {
		t.Error("s3.Stop should be called")
	}

	t.Log("✓ StopAll continues stopping other services on error")
}

func TestServiceStatusString(t *testing.T) {
	tests := []struct {
		status ServiceStatus
		want   string
	}{
		{StatusStopped, "stopped"},
		{StatusStarting, "starting"},
		{StatusRunning, "running"},
		{StatusStopping, "stopping"},
		{ServiceStatus(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.want {
			t.Errorf("Status %d: got %q, want %q", tt.status, got, tt.want)
		}
	}

	t.Log("✓ ServiceStatus.String returns correct values")
}

func TestStartAllContextCancellation(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	s1.startDelay = 100 * time.Millisecond

	_ = c.Register(s1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := c.StartAll(ctx)
	if err == nil {
		t.Error("StartAll should fail on cancelled context")
	}

	t.Log("✓ StartAll respects context cancellation")
}

func TestRegisterWhileRunning(t *testing.T) {
	c := NewServiceContainer()

	s1 := newMockService("service1")
	_ = c.Register(s1)

	ctx := context.Background()
	_ = c.StartAll(ctx)

	s2 := newMockService("service2")
	err := c.Register(s2)
	if err == nil {
		t.Error("Register should fail while container is running")
	}

	_ = c.StopAll(ctx)
	t.Log("✓ Register fails while container is running")
}

func TestEmptyContainer(t *testing.T) {
	c := NewServiceContainer()

	ctx := context.Background()

	// StartAll on empty container should succeed
	if err := c.StartAll(ctx); err != nil {
		t.Errorf("StartAll on empty container failed: %v", err)
	}

	// StopAll on empty container should succeed
	if err := c.StopAll(ctx); err != nil {
		t.Errorf("StopAll on empty container failed: %v", err)
	}

	t.Log("✓ Empty container operations succeed")
}
