package txspool

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestFalconAdmissionRequiresActivationAndVerifiedSender(t *testing.T) {
	blob, err := os.ReadFile("../../common/transaction/testdata/falcon512_wallet_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(blob, &v); err != nil {
		t.Fatal(err)
	}
	raw := hexutil.MustDecode(v["rawTransaction"].(string))
	for _, tc := range []struct {
		name                 string
		fork                 *big.Int
		height               uint64
		forged, broken, want bool
	}{
		{name: "disabled", height: 10},
		{name: "before fork", fork: big.NewInt(10), height: 9},
		{name: "active", fork: big.NewInt(10), height: 10, want: true},
		{name: "forged sender", fork: big.NewInt(10), height: 10, forged: true},
		{name: "bad signature", fork: big.NewInt(10), height: 10, broken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := append([]byte(nil), raw...)
			if tc.broken {
				wire[len(wire)-1] ^= 1
			}
			tx, err := transaction.DecodeEthereumTransaction(wire)
			if err != nil {
				t.Fatal(err)
			}
			if tc.forged {
				tx.SetFrom(types.Address{0xab})
			}
			pool := &TxsPool{chainconfig: &params.ChainConfig{ChainID: big.NewInt(94), LondonBlock: big.NewInt(0), PQTxBlock: tc.fork}, eip1559: true,
				bc: &txpoolChainStub{current: &txpoolBlockStub{header: &txpoolHeaderStub{number: uint256.NewInt(tc.height)}}}}
			if got := pool.validateSender(tx); got != tc.want {
				t.Fatalf("admission %v, want %v", got, tc.want)
			}
			if tc.want && tx.From() == nil {
				t.Fatal("verified sender missing")
			}
		})
	}
}
