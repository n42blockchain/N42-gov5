package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

type auditFailedLagDB struct {
	kv.RwDB
	err error
}

func (db auditFailedLagDB) Update(context.Context, func(kv.RwTx) error) error { return db.err }

func TestAuditFailedLagReloadKeepsJoinBarrier(t *testing.T) {
	failure := errors.New("injected database reload failure")
	bc := &BlockChain{ctx: context.Background(), ChainDB: auditFailedLagDB{err: failure}, qmdbRootComputer: commitment.NewQMDBRootComputer()}
	j := &lagWriteJob{number: 42, done: make(chan struct{}), err: errors.New("injected write failure")}
	close(j.done)
	bc.lagWrite.inflight = j
	if err := bc.joinLagWrite(); !errors.Is(err, failure) {
		t.Fatalf("reload failure lost: %v", err)
	}
	if bc.lagWrite.inflight != j {
		t.Fatal("failed reload removed the barrier before memory matched the store")
	}
}

// A child of a failed write never overlaps it: the in-import re-check does
// not let it skip the join that recovers the tree.
func TestAuditFailedLagWriteRefusesOverlap(t *testing.T) {
	bc := &BlockChain{ctx: context.Background(), qmdbRootComputer: commitment.NewQMDBRootComputer()}
	j := &lagWriteJob{number: 42, done: make(chan struct{}), root: bc.qmdbRootComputer.Root()}
	bc.lagWrite.inflight = j
	if !bc.lagWriteFastReady() {
		t.Fatal("a write in flight on its own root must allow the overlap")
	}
	j.err = errors.New("injected write failure")
	close(j.done)
	if bc.lagWriteFastReady() {
		t.Fatal("the child of a failed write overlapped it")
	}
}
