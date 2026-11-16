// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"
	"time"
)

// The idle split: a pass that met an unready parent, a tick sleep, then the
// pick; the next import starts from a clean split.
func TestLagIdleSplit(t *testing.T) {
	var li lagIdle
	li.done(nil)
	end := li.prevEnd
	li.passBegin()
	li.passEmpty("parent")
	li.slept(3*time.Millisecond, "tick")
	li.passBegin()
	s := li.pick(end.Add(time.Millisecond))
	if s.passes != 1 || s.skipWhy != "parent" || s.wake != "tick" || s.wait != 3*time.Millisecond || s.qdAfter != time.Millisecond {
		t.Fatalf("split %+v", s)
	}
	s.lockAt = time.Now()
	f := s.fields(time.Now())
	if len(f) != 22 || f[0] != "impGapUs" || f[1].(int64) < 0 {
		t.Fatalf("fields %v", f)
	}
	s.loggedAt, s.loggedSet = time.Now(), true
	li.done(s)
	n := li.pick(time.Time{})
	if n.passes != 0 || n.skipWhy != "" || n.wake != "loop" || n.wait != 0 || n.prevTail < 0 {
		t.Fatalf("not reset: %+v", n)
	}
	if (*lagIdleSnap)(nil).fields(time.Now()) != nil {
		t.Fatal("nil snap has fields")
	}
}
