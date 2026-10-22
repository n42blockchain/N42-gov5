package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type headerReadDB struct {
	kv.RwDB
	bodyReads atomic.Int64
	failBody  atomic.Bool
	failView  atomic.Bool
}

type headerReadTx struct {
	kv.Tx
	db *headerReadDB
}

func (d *headerReadDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	if d.failView.Load() {
		return errors.New("injected view failure")
	}
	return d.RwDB.View(ctx, func(tx kv.Tx) error { return fn(&headerReadTx{Tx: tx, db: d}) })
}

func (t *headerReadTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.BlockBody {
		t.db.bodyReads.Add(1)
		if t.db.failBody.Load() {
			return nil, errors.New("availability body read forbidden")
		}
	}
	return t.Tx.GetOne(table, key)
}

func headerReadFixture(t *testing.T) (*Replica, *headerReadDB) {
	t.Helper()
	physical := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).MustOpen()
	t.Cleanup(physical.Close)
	db := &headerReadDB{RwDB: physical}
	genesis := block.NewBlock(&block.Header{Number: uint256.NewInt(0), Difficulty: uint256.NewInt(1), GasLimit: 1000000}, nil).(*block.Block)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, genesis); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, genesis.Hash())
		return rawdb.WriteCanonicalHash(tx, genesis.Hash(), 0)
	}); err != nil {
		t.Fatal(err)
	}
	return &Replica{ctx: context.Background(), db: db, genesis: genesis}, db
}

func TestCanonicalHeaderDoesNotReadAvailabilityBody(t *testing.T) {
	r, db := headerReadFixture(t)
	header := &block.Header{Number: uint256.NewInt(7), Difficulty: uint256.NewInt(1), ParentHash: r.genesis.Hash(), GasLimit: 1000000}
	if err := db.Update(r.ctx, func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, header)
		rawdb.WriteHeadBlockHash(tx, header.Hash())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db.failBody.Store(true)
	if got := r.currentHeader(); got.Hash() != header.Hash() || got.Number.Uint64() != 7 {
		t.Fatal("header-only canonical read lost head metadata without a body")
	}
	n := &Network{replica: r}
	n.maxHeight.Store(11)
	if got := n.HeightBehind(); got != 4 {
		t.Fatalf("height lag: got %d want4", got)
	}
	if got := db.bodyReads.Load(); got != 0 {
		t.Fatalf("metadata-only reads touched body %d times", got)
	}
	if got := r.CurrentBlock(); got.Hash() != r.genesis.Hash() || db.bodyReads.Load() == 0 {
		t.Fatal("full-block reader stopped requiring physical availability")
	}
	// A fresh call must observe the next canonical head; this is not a cache.
	if err := db.Update(r.ctx, func(tx kv.RwTx) error {
		rawdb.WriteHeadBlockHash(tx, r.genesis.Hash())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.currentHeader(); got.Hash() != r.genesis.Hash() {
		t.Fatal("header-only read reused an old head")
	}
}

func TestCanonicalHeaderFallbackOnMissingOrUnreadableMetadata(t *testing.T) {
	r, db := headerReadFixture(t)
	if err := db.Update(r.ctx, func(tx kv.RwTx) error {
		rawdb.WriteHeadBlockHash(tx, types.Hash{9})
		return rawdb.WriteHeaderNumber(tx, types.Hash{9}, 3)
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.currentHeader(); got.Hash() != r.genesis.Hash() {
		t.Fatal("missing canonical header did not use existing genesis fallback")
	}
	db.failView.Store(true)
	if got := r.currentHeader(); got.Hash() != r.genesis.Hash() {
		t.Fatal("view failure did not use existing genesis fallback")
	}
}

type headerTestStream struct {
	network.Stream
	request *bytes.Reader
	reply   bytes.Buffer
}

func (s *headerTestStream) Read(p []byte) (int, error)  { return s.request.Read(p) }
func (s *headerTestStream) Write(p []byte) (int, error) { return s.reply.Write(p) }
func (s *headerTestStream) SetDeadline(time.Time) error { return nil }
func (s *headerTestStream) Close() error                { return nil }
func (s *headerTestStream) CloseWrite() error           { return nil }
func (s *headerTestStream) Reset() error                { return nil }

func TestCanonicalHeaderStatusKeepsFullBodyRequests(t *testing.T) {
	r, db := headerReadFixture(t)
	for _, kind := range []byte{requestStatus, requestHash, requestNumber} {
		var request [33]byte
		request[0] = kind
		if kind == requestHash {
			hash := r.genesis.Hash()
			copy(request[1:], hash[:])
		} else if kind == requestNumber {
			binary.BigEndian.PutUint64(request[1:9], 0)
		}
		n := &Network{replica: r, ctx: r.ctx, serveSlots: make(chan struct{}, 1), streams: make(map[network.Stream]struct{})}
		s := &headerTestStream{request: bytes.NewReader(request[:])}
		before := db.bodyReads.Load()
		n.serve(s)
		meta, err := readFrame(&s.reply, maxReplyMetadata)
		if err != nil {
			t.Fatal(err)
		}
		var reply bodyReply
		if err := json.Unmarshal(meta, &reply); err != nil {
			t.Fatal(err)
		}
		var header block.Header
		if err := rlp.DecodeBytes(reply.Header, &header); err != nil || header.Hash() != r.genesis.Hash() || reply.Certificate != nil {
			t.Fatalf("header/QC reply differs: %v", err)
		}
		raw, err := readFrame(&s.reply, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if kind == requestStatus {
			if len(raw) != 0 || db.bodyReads.Load() != before {
				t.Fatal("status request decoded or returned an availability body")
			}
		} else {
			var full block.Block
			if len(raw) == 0 || full.Unmarshal(raw) != nil || full.Hash() != r.genesis.Hash() || db.bodyReads.Load() == before {
				t.Fatal("full block request lost physical body or wire encoding")
			}
		}
	}
}
