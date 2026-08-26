// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package tracers_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/tracers"
)

// blockscoutRequired is the JSON-RPC surface Blockscout calls on an archive
// node, from docs.blockscout.com/setup/requirements/node-tracing-json-rpc-requirements
// as of backend v11.2.8.
//
// trace_ carries internal transactions for the Erigon/Nethermind variant and
// debug_ does the same for the Geth variant; Blockscout picks one by
// ETHEREUM_JSONRPC_VARIANT, and this node advertises both, so both must stay.
// txpool_content is what backs the pending-transactions view.
var blockscoutRequired = map[string][]string{
	"eth": {
		"blockNumber",
		"call",
		"getBalance",
		"getCode",
		"getBlockByHash",
		"getBlockByNumber",
		"getTransactionByHash",
		"getTransactionByBlockHashAndIndex",
		"getTransactionByBlockNumberAndIndex",
		"getTransactionReceipt",
		"getUncleByBlockHashAndIndex",
		"getLogs",
	},
	"trace": {
		"block",
		"replayBlockTransactions",
	},
	"debug": {
		"traceBlockByNumber",
		"traceTransaction",
	},
	"txpool": {
		"content",
	},
	"net":  {"version"},
	"web3": {"clientVersion"},
}

// TestBlockscoutRPCSurface reads the registration tables the node actually
// serves from and fails if a method Blockscout depends on stopped being
// exported.
//
// It deliberately reflects over Apis()/APIs() rather than a hand-listed set of
// types: a guard that inspects something other than the path in production is
// how a removed method reaches an explorer unnoticed. The node assembles its
// table from exactly these two calls (node.go: n.api.Apis() and
// tracers.APIs(n.api)).
func TestBlockscoutRPCSurface(t *testing.T) {
	served := map[string]map[string]bool{}
	add := func(ns string, service interface{}) {
		if served[ns] == nil {
			served[ns] = map[string]bool{}
		}
		typ := reflect.TypeOf(service)
		for i := 0; i < typ.NumMethod(); i++ {
			served[ns][rpcName(typ.Method(i).Name)] = true
		}
	}

	// Zero values are enough: every constructor here only stores the pointer,
	// and reflection reads the method set, not the receiver's contents.
	for _, a := range (&api.API{}).Apis() {
		add(a.Namespace, a.Service)
	}
	for _, a := range tracers.APIs(nil) {
		add(a.Namespace, a.Service)
	}

	total := 0
	for _, m := range served {
		total += len(m)
	}
	t.Logf("registration table exposes %d methods across %d namespaces", total, len(served))

	var missing []string
	for ns, methods := range blockscoutRequired {
		for _, m := range methods {
			if !served[ns][m] {
				missing = append(missing, ns+"_"+m)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("Blockscout requires these JSON-RPC methods and the node no longer "+
			"exports them:\n  %s", strings.Join(missing, "\n  "))
	}
}

// rpcName maps a Go method name to the name the JSON-RPC server publishes,
// mirroring jsonrpc.formatName.
func rpcName(name string) string {
	r := []rune(name)
	if len(r) > 0 {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}
