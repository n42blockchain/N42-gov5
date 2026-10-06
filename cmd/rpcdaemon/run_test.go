package main

import (
	"flag"
	"testing"
	"time"

	"github.com/urfave/cli/v2"
)

// TestRPCDaemonRunFailsWithoutCoreNode exercises runRPCDaemon's connection
// preflight: with no gRPC core node listening at the configured address, the
// remote-DB handshake must fail quickly and runRPCDaemon must return that
// error rather than hang or panic. This covers the dial + remotedb.Open()
// error path without starting any real listener or node.
func TestRPCDaemonRunFailsWithoutCoreNode(t *testing.T) {
	app := cli.NewApp()
	app.Flags = []cli.Flag{privateAPIFlag, httpAddrFlag, httpPortFlag}
	set := flag.NewFlagSet("test", 0)
	set.String(privateAPIFlag.Name, "", "")
	set.String(httpAddrFlag.Name, "", "")
	set.Int(httpPortFlag.Name, 0, "")
	ctx := cli.NewContext(app, set, nil)
	// Port 1 is a privileged, essentially always-unbound port on loopback --
	// connections to it refuse immediately rather than timing out.
	if err := set.Set(privateAPIFlag.Name, "127.0.0.1:1"); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- runRPCDaemon(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected runRPCDaemon to fail when no core node is reachable")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runRPCDaemon did not return within 20s against an unreachable core node")
	}
}
