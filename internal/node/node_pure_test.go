package node

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/conf"
)

func TestExplicitInitRequiredError(t *testing.T) {
	err := explicitInitRequiredError("mainnet", "/data/n42")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mainnet")
	require.Contains(t, err.Error(), "/data/n42")

	err = explicitInitRequiredError("mainnet", "   ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "<datadir>")
}

func TestNodeInstanceDir(t *testing.T) {
	n := &Node{config: &conf.Config{NodeCfg: conf.NodeConfig{DataDir: "/tmp/somewhere"}}}
	require.Equal(t, "/tmp/somewhere", n.InstanceDir())
}

func TestNodeSimpleGetters(t *testing.T) {
	n := &Node{}
	require.Nil(t, n.BlockChain())
	require.Nil(t, n.Database())
	require.Nil(t, n.AccountManager())
}

func TestNodeWaitUnblocksOnShutdown(t *testing.T) {
	n := &Node{shutDown: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		n.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Wait returned before shutDown was closed")
	case <-time.After(20 * time.Millisecond):
	}

	close(n.shutDown)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not unblock after shutDown was closed")
	}
}
