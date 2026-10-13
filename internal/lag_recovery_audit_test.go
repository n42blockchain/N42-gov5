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
