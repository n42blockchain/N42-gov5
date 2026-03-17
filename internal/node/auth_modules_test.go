package node

import (
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestAuthenticatedModulesSkipsOpenNamespaces(t *testing.T) {
	t.Parallel()

	apis := []jsonrpc.API{
		{Namespace: "eth"},
		{Namespace: "engine", Authenticated: true},
		{Namespace: "debug"},
	}
	got := authenticatedModules(apis)
	want := []string{"engine"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authenticatedModules() = %v, want %v", got, want)
	}
}

func TestAuthenticatedModulesDeduplicatesInOrder(t *testing.T) {
	t.Parallel()

	apis := []jsonrpc.API{
		{Namespace: "engine", Authenticated: true},
		{Namespace: "apos", Authenticated: true},
		{Namespace: "engine", Authenticated: true},
	}
	got := authenticatedModules(apis)
	want := []string{"engine", "apos"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authenticatedModules() = %v, want %v", got, want)
	}
}
