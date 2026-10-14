package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestIndependentVoteStoreMarkerFailsClosedOnAnyStoredValue(t *testing.T) {
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if present, err := HasIndependentVoteStore(tx); err != nil || present {
		t.Fatalf("missing marker: present=%v err=%v", present, err)
	}
	for _, value := range [][]byte{{0}, []byte("malformed"), []byte("binding")} {
		if err := tx.Put(modules.HotStuffState, voteStoreKey, value); err != nil {
			t.Fatal(err)
		}
		if present, err := HasIndependentVoteStore(tx); err != nil || !present {
			t.Fatalf("stored marker %q: present=%v err=%v", value, present, err)
		}
	}
}
