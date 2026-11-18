package datc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
)

type auditFailedArchiveDB struct {
	kv.RoDB
	err error
}

func (db auditFailedArchiveDB) BeginRo(context.Context) (kv.Tx, error) { return nil, db.err }

func TestAuditArchiveRefreshFailureRejectsCachedReads(t *testing.T) {
	failure := errors.New("injected archive refresh failure")
	a := &Archive{db: auditFailedArchiveDB{err: failure}, head: 10, gen: 1, pool: make(chan *pooledReader, 1)}
	a.pool <- &pooledReader{gen: 1}
	called := false
	err := a.withReader(context.Background(), 1, func(*querier) error { called = true; return nil })
	if !errors.Is(err, failure) || called {
		t.Fatalf("refresh failure did not stop cached read: called=%v err=%v", called, err)
	}
	if !a.checkedAt.IsZero() {
		t.Fatal("failed refresh suppresses retries")
	}
}

func TestAuditArchiveReaderLimitHonorsCancellation(t *testing.T) {
	a := &Archive{head: 10, gen: 1, opts: ArchiveOptions{Readers: 1, RefreshEvery: -1}, pool: make(chan *pooledReader, 2)}
	a.pool <- &pooledReader{gen: 1}
	a.pool <- &pooledReader{gen: 1}
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- a.withReader(context.Background(), 1, func(*querier) error { close(started); <-finish; return nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	called := false
	err := a.withReader(ctx, 1, func(*querier) error { called = true; return nil })
	close(finish)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("reader limit bypassed: called=%v err=%v", called, err)
	}
}
