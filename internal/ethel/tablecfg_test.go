package ethel

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

func TestChainTableCfgMergesStandardAndN42Tables(t *testing.T) {
	standard := kv.TableCfg{kv.SyncStageProgress: {}}
	merged := ChainTableCfg(standard)
	if _, ok := merged[kv.SyncStageProgress]; !ok {
		t.Fatal("merged table config is missing SyncStageProgress")
	}
	if _, ok := merged[modules.TrieOfAccounts]; !ok {
		t.Fatal("merged table config is missing TrieOfAccounts")
	}
}
