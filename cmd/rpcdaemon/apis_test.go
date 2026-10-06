package main

import "testing"

func TestRPCDaemonWeb3APIClientVersion(t *testing.T) {
	api := &Web3API{version: "9.9.9"}
	got := api.ClientVersion()
	want := "N42-RPCDaemon/v9.9.9"
	if got != want {
		t.Fatalf("ClientVersion() = %q, want %q", got, want)
	}
}

func TestRPCDaemonNetAPIVersion(t *testing.T) {
	api := &NetAPI{}
	if got := api.Version(); got != "1" {
		t.Fatalf("Version() = %q, want %q", got, "1")
	}
}

func TestRPCDaemonNetAPIListening(t *testing.T) {
	api := &NetAPI{}
	if !api.Listening() {
		t.Fatal("Listening() = false, want true")
	}
}
