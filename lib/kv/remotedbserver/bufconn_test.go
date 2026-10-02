package remotedbserver

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// newBufconnFixture starts a gRPC server exposing the given KvServer over an
// in-memory bufconn listener, and returns a connected remote.KVClient plus a
// cleanup func that tears down the server and the connection.
func newBufconnFixture(t *testing.T, s *KvServer) remote.KVClient {
	t.Helper()
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)

	grpcServer := grpc.NewServer()
	remote.RegisterKVServer(grpcServer, s)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dialing bufnet: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
	})

	return remote.NewKVClient(conn)
}

// newServerWithData builds a KvServer over a fresh memdb preloaded with a
// small PlainState dataset (one DupSort key with two values, plus two more
// single-value keys), useful for exercising cursor ops end to end.
func newServerWithData(t *testing.T) (*KvServer, kv.RwDB) {
	t.Helper()
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		wc, err := tx.RwCursorDupSort(kv.PlainState)
		if err != nil {
			return err
		}
		if err := wc.Append([]byte{1}, []byte{1}); err != nil {
			return err
		}
		if err := wc.Append([]byte{1}, []byte{2}); err != nil {
			return err
		}
		if err := wc.Append([]byte{2}, []byte{1}); err != nil {
			return err
		}
		if err := wc.Append([]byte{3}, []byte{1}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	return NewKvServer(ctx, db, nil, nil, nil, log.New()), db
}

// memdbNewPlainStateWithNKeys builds a fresh memdb preloaded with n
// sequentially-numbered single-value keys in PlainState, used to force
// pagination thresholds (PageSizeLimit) in Range/IndexRange.
func memdbNewPlainStateWithNKeys(t *testing.T, n int) kv.RwDB {
	t.Helper()
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		wc, err := tx.RwCursor(kv.PlainState)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			k := []byte{byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i)}
			if err := wc.Append(k, []byte("v")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	return db
}
