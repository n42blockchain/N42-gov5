package filters

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// Every exported method of a registered service is an RPC method, so an
// exported lifecycle method would let any client stop filter expiry for good.
func TestFilterShutdownIsNotAnRPCMethod(t *testing.T) {
	fa := NewFilterAPI(g41NewAPI(), time.Hour)
	defer fa.close()

	srv := jsonrpc.NewServer()
	defer srv.Stop()
	if err := srv.RegisterName("eth", fa); err != nil {
		t.Fatal(err)
	}
	client := jsonrpc.DialInProc(srv)
	defer client.Close()

	err := client.Call(nil, "eth_close")
	if err == nil {
		t.Fatal("eth_close was served")
	}
	if fa.ctx.Err() != nil {
		t.Fatal("eth_close stopped the filter expiry loop")
	}
}
