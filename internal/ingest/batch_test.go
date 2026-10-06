package ingest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

type batchingTestPool struct {
	groups      [][]*transaction.Transaction
	badVerdicts bool
}

func (*batchingTestPool) AddLocal(*transaction.Transaction) error { panic("single admission used") }
func (*batchingTestPool) Stats() (int, int, int, int)             { return 0, 0, 0, 0 }
func (p *batchingTestPool) AddLocals(txs []*transaction.Transaction) []error {
	// Deliberately retain the argument to catch unsafe slice reuse.
	p.groups = append(p.groups, txs)
	if p.badVerdicts {
		return nil
	}
	errs := make([]error, len(txs))
	for i, tx := range txs {
		if tx.Nonce()%7 == 0 {
			errs[i] = errors.New("rejected")
		}
	}
	return errs
}

func batchFixture(t *testing.T, count, dataSize int) ([]byte, uint32) {
	t.Helper()
	frame := binary.LittleEndian.AppendUint32(nil, uint32(count))
	var accepted uint32
	for i := 0; i < count; i++ {
		from := types.Address{byte(i), byte(i >> 8)}
		to := types.Address{0x99}
		tx := transaction.NewTransaction(uint64(i), from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), bytes.Repeat([]byte{byte(i)}, dataSize))
		raw, err := tx.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > maxTxSize {
			t.Fatal("fixture transaction too large")
		}
		frame = binary.LittleEndian.AppendUint16(frame, uint16(len(raw)))
		frame = append(frame, raw...)
		frame = append(frame, from[:]...)
		if i%7 != 0 {
			accepted++
		}
	}
	return frame, accepted
}

func TestIngestBoundedBatchAdmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count, data int
	}{
		{"count bound", 2*poolBatchTransactions + 13, 8},
		{"byte bound", 60, 40000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame, want := batchFixture(t, tc.count, tc.data)
			pool := new(batchingTestPool)
			s := NewServer("", pool, 1000, 1000)
			got, err := s.readBatch(bytes.NewReader(frame))
			if err != nil || got != want {
				t.Fatalf("accepted=%d want=%d err=%v", got, want, err)
			}
			if len(pool.groups) < 2 {
				t.Fatal("batch was not split")
			}
			index := 0
			for _, group := range pool.groups {
				if len(group) > poolBatchTransactions {
					t.Fatal("transaction bound exceeded")
				}
				nativeBytes := 0
				for _, tx := range group {
					if tx == nil || tx.Nonce() != uint64(index) || *tx.From() != (types.Address{byte(index), byte(index >> 8)}) || !bytes.Equal(tx.Data(), bytes.Repeat([]byte{byte(index)}, tc.data)) {
						t.Fatalf("transaction %d changed", index)
					}
					raw, err := tx.Marshal()
					if err != nil {
						t.Fatal(err)
					}
					nativeBytes += len(raw)
					index++
				}
				if nativeBytes > poolBatchBytes {
					t.Fatal("byte bound exceeded")
				}
			}
			if index != tc.count {
				t.Fatalf("lost transactions: %d", index)
			}
			injected, rejected, batches := s.Stats()
			if injected != uint64(want) || rejected != uint64(tc.count)-uint64(want) || batches != 1 {
				t.Fatalf("wrong stats %d/%d/%d", injected, rejected, batches)
			}
		})
	}
}

func TestIngestBatchTruncatedFramePreservesCompleteTransactions(t *testing.T) {
	frame, want := batchFixture(t, 19, 8)
	// Promise one more transaction, then truncate its length field.
	binary.LittleEndian.PutUint32(frame[:4], 20)
	frame = append(frame, 1)
	pool := new(batchingTestPool)
	s := NewServer("", pool, 1000, 1000)
	got, err := s.readBatch(bytes.NewReader(frame))
	if !errors.Is(err, io.ErrUnexpectedEOF) || got != want || len(pool.groups) != 1 || len(pool.groups[0]) != 19 {
		t.Fatalf("accepted=%d err=%v groups=%d", got, err, len(pool.groups))
	}
}

func TestIngestBatchInvalidVerdictCount(t *testing.T) {
	frame, _ := batchFixture(t, 3, 0)
	pool := &batchingTestPool{badVerdicts: true}
	s := NewServer("", pool, 1000, 1000)
	if accepted, err := s.readBatch(bytes.NewReader(frame)); err == nil || accepted != 0 {
		t.Fatalf("accepted=%d err=%v", accepted, err)
	}
	if len(pool.groups) != 1 {
		t.Fatal("uncertain admission was retried")
	}
}

func TestIngestStopClosesPartialConnection(t *testing.T) {
	s := NewServer("127.0.0.1:0", new(mockTxPool), 100, 1000)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if err := s.Start(); err == nil {
		t.Fatal("second listener was allowed")
	}
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// An acknowledged empty batch proves the server registered this stream.
	if _, err := conn.Write(make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	if _, err := conn.Read(reply[:]); err == nil {
		t.Fatal("stream remained open")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("shutdown left a blocked reader")
	}
	if err := s.Start(); err == nil {
		t.Fatal("cancelled server restarted")
	}
}

type blockedBatchPool struct {
	mockTxPool
	entered chan struct{}
	release chan struct{}
}

func (p *blockedBatchPool) AddLocals(txs []*transaction.Transaction) []error {
	close(p.entered)
	<-p.release
	return make([]error, len(txs))
}

func TestIngestStopWaitsForPoolSubmission(t *testing.T) {
	pool := &blockedBatchPool{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(pool.release) }) }
	s := NewServer("127.0.0.1:0", pool, 100, 1000)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unblock(); s.Stop() }()
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	frame, _ := batchFixture(t, 1, 0)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pool.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not reach pool")
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	var reply [4]byte
	if _, err := conn.Read(reply[:]); err == nil {
		t.Fatal("shutdown did not close connection")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("shutdown left connection open")
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned while the pool still had an active submission")
	default:
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after admission completed")
	}
}
