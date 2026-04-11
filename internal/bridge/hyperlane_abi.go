// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Minimal Hyperlane IMailbox ABI definitions used by the dispatcher
// and receiver. Exposes only the functions the bridge actually needs:
// dispatch, quoteDispatch, localDomain, latestDispatchedId and the
// corresponding event topics. Avoids pulling in the full Hyperlane
// client dependency tree.

package bridge

// hyperlaneMailboxABI is the minimal ABI for Hyperlane IMailbox.
// Only includes the functions needed for dispatching and quoting messages.
const hyperlaneMailboxABI = `[
	{
		"name": "dispatch",
		"type": "function",
		"inputs": [
			{"name": "destinationDomain", "type": "uint32"},
			{"name": "recipientAddress", "type": "bytes32"},
			{"name": "messageBody", "type": "bytes"}
		],
		"outputs": [{"name": "messageId", "type": "bytes32"}]
	},
	{
		"name": "quoteDispatch",
		"type": "function",
		"inputs": [
			{"name": "destinationDomain", "type": "uint32"},
			{"name": "messageBody", "type": "bytes"}
		],
		"outputs": [{"name": "fee", "type": "uint256"}]
	},
	{
		"name": "localDomain",
		"type": "function",
		"inputs": [],
		"outputs": [{"name": "", "type": "uint32"}]
	},
	{
		"name": "latestDispatchedId",
		"type": "function",
		"inputs": [],
		"outputs": [{"name": "", "type": "bytes32"}]
	},
	{
		"anonymous": false,
		"name": "Dispatch",
		"type": "event",
		"inputs": [
			{"name": "sender", "type": "address", "indexed": true},
			{"name": "destination", "type": "uint32", "indexed": true},
			{"name": "recipient", "type": "bytes32", "indexed": true},
			{"name": "message", "type": "bytes", "indexed": false}
		]
	},
	{
		"anonymous": false,
		"name": "Process",
		"type": "event",
		"inputs": [
			{"name": "origin", "type": "uint32", "indexed": true},
			{"name": "sender", "type": "bytes32", "indexed": true},
			{"name": "recipient", "type": "address", "indexed": true}
		]
	}
]`
