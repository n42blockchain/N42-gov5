package wrap

import (
	"github.com/n42blockchain/N42/lib/kv"
)

type TxContainer struct {
	Tx  kv.RwTx
	Ttx kv.TemporalTx
}
