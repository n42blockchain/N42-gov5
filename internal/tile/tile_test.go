// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package tile

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestTile_StartStop(t *testing.T) {
	var count atomic.Int64
	fn := func(ctx context.Context, io *TileIO) error {
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
				count.Add(1)
				runtime.Gosched()
			}
		}
	}

	tile := NewTile(TileConfig{
		ID: "test",
		Fn: fn,
		IO: NewTileIO(nil, nil),
	})

	tile.Start(context.Background())
	time.Sleep(50 * time.Millisecond)

	if tile.State() != TileRunning {
		t.Fatalf("expected running, got %d", tile.State())
	}

	tile.Stop()

	if tile.State() != TileStopped {
		t.Fatalf("expected stopped, got %d", tile.State())
	}
	if count.Load() == 0 {
		t.Error("tile function should have executed at least once")
	}
}

func TestTile_PanicRecovery(t *testing.T) {
	calls := atomic.Int32{}
	fn := func(ctx context.Context, io *TileIO) error {
		n := calls.Add(1)
		if n <= 2 {
			panic("intentional crash")
		}
		// After 2 panics, run normally.
		<-ctx.Done()
		return nil
	}

	tile := NewTile(TileConfig{
		ID:           "crash-test",
		Fn:           fn,
		IO:           NewTileIO(nil, nil),
		MaxRestarts:  5,
		RestartDelay: 10 * time.Millisecond,
	})

	tile.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	if tile.State() != TileRunning {
		t.Fatalf("expected running after recovery, got %d", tile.State())
	}
	if tile.Stats().PanicsTotal != 2 {
		t.Errorf("expected 2 panics, got %d", tile.Stats().PanicsTotal)
	}

	tile.Stop()
}

func TestTile_MaxRestarts(t *testing.T) {
	fn := func(ctx context.Context, io *TileIO) error {
		panic("always crash")
	}

	tile := NewTile(TileConfig{
		ID:           "always-crash",
		Fn:           fn,
		IO:           NewTileIO(nil, nil),
		MaxRestarts:  2,
		RestartDelay: 5 * time.Millisecond,
	})

	tile.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	if tile.State() != TileFailed {
		t.Fatalf("expected failed after exhausting restarts, got %d", tile.State())
	}

	tile.Stop() // should not hang
}

func TestTile_IO_SendRecv(t *testing.T) {
	ring := NewRingBuffer(16)
	io := NewTileIO([]*RingBuffer{ring}, []*RingBuffer{ring})

	if !io.Send(0, "hello") {
		t.Fatal("send should succeed")
	}

	item, ok := io.Recv(0)
	if !ok || item.(string) != "hello" {
		t.Fatalf("expected 'hello', got %v", item)
	}
}

func TestTile_IO_InvalidIndex(t *testing.T) {
	io := NewTileIO(nil, nil)

	_, ok := io.Recv(0)
	if ok {
		t.Error("recv from nil inputs should return false")
	}

	if io.Send(0, "x") {
		t.Error("send to nil outputs should return false")
	}
}

func TestManager_SetupPipeline(t *testing.T) {
	noop := func(ctx context.Context, io *TileIO) error {
		<-ctx.Done()
		return nil
	}

	mgr := NewManager(ManagerConfig{
		Enabled:      true,
		RingSize:     64,
		MaxRestarts:  1,
		RestartDelay: 10 * time.Millisecond,
	})

	mgr.SetupPipeline(noop, noop, noop, noop, noop)

	if mgr.TileCount() != 5 {
		t.Fatalf("expected 5 tiles, got %d", mgr.TileCount())
	}

	ringStats := mgr.RingStats()
	if len(ringStats) != 4 {
		t.Fatalf("expected 4 rings, got %d", len(ringStats))
	}

	mgr.Start(context.Background())
	time.Sleep(50 * time.Millisecond)

	stats := mgr.Stats()
	for _, s := range stats {
		if s.State != TileRunning {
			t.Errorf("tile %s should be running, got %d", s.ID, s.State)
		}
	}

	mgr.Stop()

	stats = mgr.Stats()
	for _, s := range stats {
		if s.State != TileStopped {
			t.Errorf("tile %s should be stopped, got %d", s.ID, s.State)
		}
	}
}

func TestManager_EndToEnd(t *testing.T) {
	var result atomic.Int64

	// Simple 3-tile pipeline: producer → processor → consumer
	mgr := NewManager(ManagerConfig{
		Enabled:  true,
		RingSize: 64,
	})

	r1 := mgr.CreateRing()
	r2 := mgr.CreateRing()

	// Producer: sends 100 items
	mgr.AddTile("producer", func(ctx context.Context, io *TileIO) error {
		for i := 0; i < 100; i++ {
			for !io.Send(0, i) {
				if ctx.Err() != nil {
					return nil
				}
				runtime.Gosched()
			}
		}
		<-ctx.Done()
		return nil
	}, nil, []*RingBuffer{r1})

	// Processor: doubles each item
	mgr.AddTile("processor", func(ctx context.Context, io *TileIO) error {
		for {
			item, ok := io.Recv(0)
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				runtime.Gosched()
				continue
			}
			v := item.(int) * 2
			for !io.Send(0, v) {
				if ctx.Err() != nil {
					return nil
				}
				runtime.Gosched()
			}
		}
	}, []*RingBuffer{r1}, []*RingBuffer{r2})

	// Consumer: sums all items
	mgr.AddTile("consumer", func(ctx context.Context, io *TileIO) error {
		for {
			item, ok := io.Recv(0)
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				runtime.Gosched()
				continue
			}
			result.Add(int64(item.(int)))
		}
	}, []*RingBuffer{r2}, nil)

	mgr.Start(context.Background())
	time.Sleep(500 * time.Millisecond)
	mgr.Stop()

	// Sum of 2*i for i=0..99 = 2 * (99*100/2) = 9900
	expected := int64(9900)
	got := result.Load()
	if got != expected {
		t.Errorf("expected sum %d, got %d", expected, got)
	}
}

func TestManager_Reset(t *testing.T) {
	var count atomic.Int64
	noop := func(ctx context.Context, io *TileIO) error {
		count.Add(1)
		<-ctx.Done()
		return nil
	}

	mgr := NewManager(ManagerConfig{
		Enabled:  true,
		RingSize: 16,
	})
	mgr.SetupPipeline(noop, noop, noop, noop, noop)
	mgr.Start(context.Background())
	time.Sleep(50 * time.Millisecond)

	mgr.Reset(context.Background())
	time.Sleep(50 * time.Millisecond)

	// All tiles should have started at least twice (before + after reset).
	if count.Load() < 10 { // 5 tiles * 2 rounds
		t.Errorf("expected at least 10 tile starts, got %d", count.Load())
	}

	mgr.Stop()
}
