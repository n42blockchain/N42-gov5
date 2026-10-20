package sharedvalidator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type statusBlockingVoteJournal struct {
	entered chan struct{}
	release chan struct{}
}

func (j *statusBlockingVoteJournal) JournalVote(*hotstuff.ConsensusState) error {
	close(j.entered)
	<-j.release
	return errors.New("test journal failure: no proposal may be emitted")
}

func TestStatusRemainsFreshWhileReadinessWaitsForVoteJournal(t *testing.T) {
	key, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	address := types.Address{1}
	engine := hotstuff.New(nil, nil)
	engine.Authorize(address, key)
	if err := engine.InitEngine([]hotstuff.ValidatorInfo{{Address: address, PublicKey: key.PublicKey()}}, 0); err != nil {
		t.Fatal(err)
	}
	ce := engine.Engine()
	journal := &statusBlockingVoteJournal{entered: make(chan struct{}), release: make(chan struct{})}
	ce.SetVoteJournal(journal)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(journal.release) }) }
	defer release()
	voteDone := make(chan error, 1)
	go func() {
		voteDone <- ce.ProcessEvent(hotstuff.ConsensusEvent{Type: hotstuff.EventBlockReady, Hash: types.Hash{3}, TxRootHash: types.Hash{6}})
	}()
	select {
	case <-journal.entered: // JournalVote is called with the real engine mutex held.
	case <-time.After(time.Second):
		t.Fatal("engine never entered the blocked vote journal")
	}

	// The generic memdb table set lacks the validator's HotStuffState bucket.
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(defaults kv.TableCfg) kv.TableCfg {
		tables := make(kv.TableCfg, len(defaults)+1)
		for name, config := range defaults {
			tables[name] = config
		}
		tables[modules.HotStuffState] = kv.TableCfgItem{}
		return tables
	}).MustOpen()
	t.Cleanup(db.Close)
	identity := sharedexec.Identity{Version: 1, Namespace: "status-lock-test", ChainID: "42", GenesisHash: types.Hash{1}}
	progress := CanonicalProgress{Identity: identity, Number: 1, BlockHash: types.Hash{4}, CommittedExecutedTransactions: 7}
	write := func() {
		t.Helper()
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			rawdb.WriteHeadBlockHash(tx, progress.BlockHash)
			if err := rawdb.WriteHeaderNumber(tx, progress.BlockHash, progress.Number); err != nil {
				return err
			}
			if err := rawdb.WriteCanonicalHash(tx, progress.BlockHash, progress.Number); err != nil {
				return err
			}
			return writeCanonicalProgress(tx, progress)
		}); err != nil {
			t.Fatal(err)
		}
	}
	write()
	r := &Replica{db: db, identity: identity, engine: engine}
	p := &Producer{replica: r, started: true, ownerQueue: &OwnerQueueBinding{Epoch: [16]byte{1}, ManifestHash: types.Hash{2}}}
	a, err := NewAPI(r, p)
	if err != nil {
		t.Fatal(err)
	}
	viewReadStarted := make(chan struct{})
	readyDone := make(chan struct{})
	go func() {
		p.readyForProduction(func() hotstuff.ViewNumber {
			close(viewReadStarted)
			return ce.CurrentView()
		})
		close(readyDone)
	}()
	<-viewReadStarted
	defer func() {
		release()
		<-readyDone
		if err := <-voteDone; err == nil {
			t.Error("test journal failure unexpectedly authorized a proposal")
		}
	}()

	// Change the physical progress row while the engine remains blocked. Status
	// must read this new metadata, not return a cached pre-journal observation.
	progress.Number, progress.BlockHash, progress.CommittedExecutedTransactions = 2, types.Hash{5}, 11
	write()
	type response struct {
		value StatusResult
		err   error
	}
	statusDone := make(chan response, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { value, err := a.status(ctx); statusDone <- response{value, err} }()
	select {
	case got := <-statusDone:
		if got.err != nil || got.value.CanonicalProgress != progress || !got.value.ProducerRunning || got.value.OwnerQueue == nil || *got.value.OwnerQueue != *p.ownerQueue {
			t.Fatalf("status lost fresh metadata or producer binding: %+v %v", got.value, got.err)
		}
	case <-ctx.Done():
		release()
		<-statusDone
		t.Fatal("status waited for the unrelated engine vote journal")
	}
	select {
	case <-readyDone:
		t.Fatal("readiness completed before the engine journal was released")
	default:
	}
}
