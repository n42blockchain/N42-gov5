// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// HyperlaneMailboxBinding implements HyperlaneDispatcher by calling the
// Hyperlane Mailbox contract deployed on the N42 chain.
//
// The Mailbox contract lives on N42 itself — dispatching a message means
// sending a N42 transaction to the Mailbox, which the Hyperlane relayer
// network then delivers to the destination chain.
type HyperlaneMailboxBinding struct {
	client      *jsonrpc.Client
	mailboxAddr types.Address
	mailboxABI  abi.ABI
	n42Domain   uint32
	from        types.Address
}

// NewHyperlaneMailboxBinding creates a new Hyperlane Mailbox dispatcher.
// endpoint is the N42 node's own RPC endpoint (e.g., http://localhost:20012).
func NewHyperlaneMailboxBinding(
	endpoint string,
	mailboxAddr types.Address,
	n42Domain uint32,
	from types.Address,
) (*HyperlaneMailboxBinding, error) {
	client, err := jsonrpc.DialHTTP(endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial N42 RPC %s: %w", endpoint, err)
	}

	parsed, err := abi.JSON(strings.NewReader(hyperlaneMailboxABI))
	if err != nil {
		return nil, fmt.Errorf("parse Mailbox ABI: %w", err)
	}

	return &HyperlaneMailboxBinding{
		client:      client,
		mailboxAddr: mailboxAddr,
		mailboxABI:  parsed,
		n42Domain:   n42Domain,
		from:        from,
	}, nil
}

// Dispatch sends a message through the Hyperlane Mailbox.
// Returns the Hyperlane message ID (32 bytes).
func (h *HyperlaneMailboxBinding) Dispatch(
	ctx context.Context,
	destDomain uint32,
	recipientAddr [32]byte,
	body []byte,
) (types.Hash, error) {

	// 1. Quote dispatch fee
	fee, err := h.QuoteDispatch(ctx, destDomain, body)
	if err != nil {
		log.Warn("Hyperlane quoteDispatch failed, using zero fee", "err", err)
		fee = uint256.NewInt(0)
	}

	// 2. ABI-encode dispatch(uint32, bytes32, bytes)
	calldata, err := h.mailboxABI.Pack("dispatch", destDomain, recipientAddr, body)
	if err != nil {
		return types.Hash{}, fmt.Errorf("ABI encode dispatch: %w", err)
	}

	// 3. Send N42 transaction to Mailbox contract
	txParams := map[string]interface{}{
		"from":  h.from.Hex(),
		"to":    h.mailboxAddr.Hex(),
		"data":  hexutil.Encode(calldata),
		"value": hexutil.EncodeBig(fee.ToBig()),
	}

	var txHash string
	if err := h.client.CallContext(ctx, &txHash, "eth_sendTransaction", txParams); err != nil {
		return types.Hash{}, fmt.Errorf("send dispatch tx: %w", err)
	}

	log.Info("Hyperlane message dispatched",
		"dest", destDomain,
		"recipient", fmt.Sprintf("%x", recipientAddr[:4]),
		"bodyLen", len(body),
		"txHash", txHash,
		"fee", fee,
	)

	return types.HexToHash(txHash), nil
}

// QuoteDispatch returns the gas payment required for dispatch.
func (h *HyperlaneMailboxBinding) QuoteDispatch(
	ctx context.Context,
	destDomain uint32,
	body []byte,
) (*uint256.Int, error) {

	calldata, err := h.mailboxABI.Pack("quoteDispatch", destDomain, body)
	if err != nil {
		return nil, fmt.Errorf("ABI encode quoteDispatch: %w", err)
	}

	callMsg := map[string]interface{}{
		"from": h.from.Hex(),
		"to":   h.mailboxAddr.Hex(),
		"data": hexutil.Encode(calldata),
	}

	var resultHex string
	if err := h.client.CallContext(ctx, &resultHex, "eth_call", callMsg, "latest"); err != nil {
		return nil, fmt.Errorf("quoteDispatch call: %w", err)
	}

	fee, err := hexutil.DecodeBig(resultHex)
	if err != nil {
		return nil, fmt.Errorf("decode fee: %w", err)
	}

	result, overflow := uint256.FromBig(fee)
	if overflow {
		return nil, fmt.Errorf("fee overflow")
	}
	return result, nil
}

// Close closes the underlying RPC client.
func (h *HyperlaneMailboxBinding) Close() {
	h.client.Close()
}
