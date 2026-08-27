package ethel

import (
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// ChainTableCfg merges the standard chaindata schema with N42's additional
// state and commitment tables. eth-el needs both: Engine/devp2p use legacy
// head/progress tables while execution uses N42's hashed/trie tables.
func ChainTableCfg(defaultCfg kv.TableCfg) kv.TableCfg {
	modules.N42Init()
	merged := make(kv.TableCfg, len(defaultCfg)+len(modules.N42TableCfg)+len(kv.ChaindataTables))
	for name, item := range defaultCfg {
		merged[name] = item
	}
	// Some N42 entrypoints replace kv.ChaindataTablesCfg before opening the
	// database. Restore any standard table names that replacement omitted.
	for _, name := range kv.ChaindataTables {
		if _, ok := merged[name]; !ok {
			merged[name] = kv.TableCfgItem{}
		}
	}
	for name, item := range modules.N42TableCfg {
		merged[name] = item
	}
	return merged
}
