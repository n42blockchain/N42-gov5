package remotedb

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/kv/remotedbserver"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// newTestRemoteDB starts a real remotedbserver.KvServer backed by a seeded
// memdb, serves it over an in-memory bufconn gRPC server, and opens a
// remotedb.DB client dialled through the same bufconn. This exercises the
// client package against the real wire protocol rather than a hand-rolled
// mock.
func newTestRemoteDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	backing := memdb.NewTestDB(t)
	if err := backing.Update(ctx, func(tx kv.RwTx) error {
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

	server := remotedbserver.NewKvServer(ctx, backing, nil, nil, nil, log.New())

	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	remote.RegisterKVServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(lis) }()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
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

	client := remote.NewKVClient(conn)
	db, err := NewRemote(gointerfaces.VersionFromProto(remotedbserver.KvServiceAPIVersion), log.New(), client).Open()
	if err != nil {
		t.Fatalf("open remote db: %v", err)
	}
	return db
}
