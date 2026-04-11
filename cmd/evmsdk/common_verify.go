// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Aggregate-signature structure and verification shared helpers.
// Defines AggSign (block Number, StateRoot, Sign, PublicKey,
// Address) used by the mobile verifier to report signed
// attestations back to the relay, and shared crypto / transport
// wiring (jsonrpc, common/hexutil, modules/state, sha3) used by
// verify_v2_exec and verify_hooks.

package evmsdk

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"time"

	"github.com/go-kit/kit/transport/http/jsonrpc"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	commTyp "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"golang.org/x/crypto/sha3"
)

type AggSign struct {
	Number    uint64            `json:"number"`
	StateRoot commTyp.Hash      `json:"stateRoot"`
	Sign      commTyp.Signature `json:"sign"`
	PublicKey commTyp.PublicKey `json:"publicKey"`
	Address   commTyp.Address   `json:"address"`
}

func (e *EvmEngine) verificationTaskBg() error {
	simpleLog("gen pubk")
	pubk, err := BlsPublicKey(e.PrivKey)
	if err != nil {
		simpleLog("generate public key error,", err)
		return err
	}
	simpleLog("init websocket")
	wssvr, err := NewWebSocketService(e.ServerUri, e.Account)
	if err != nil {
		return err
	}
	simpleLog("init websocket chats")
	ochan, ichan, err := wssvr.Chans(pubk.(string))
	if err != nil {
		_ = wssvr.Close()
		return err
	}
	go func() {
		defer func() {
			if err := wssvr.Close(); err != nil {
				simpleLog("close websocket service error", "err", err)
			}
			if err := recover(); err != nil {
				buf := make([]byte, 4096)
				runtime.Stack(buf, true)
				simpleLog("verification task down", "err", err)
				simpleLog(string(buf))
			}
			e.mu.Lock()
			e.EngineState = EngineStateStopped
			e.mu.Unlock()
		}()

		for {
			select {
			case <-e.ctx.Done():
				simpleLog("task has been cancelled")
				return
			case msg, ok := <-ochan:
				if !ok {
					simpleLog("task closed")
					return
				}
				entire, err := e.unwrapJSONRPC(msg)
				if err != nil {
					simpleLog("unwrap jsonrpc message error,err=", err)
					continue
				}
				resp, err := e.vertify(entire)
				if err != nil {
					simpleLog("ee verification failed", err)
					continue
				}
				if !sendVerificationResult(e.ctx, ichan, resp) {
					simpleLog("verification response dropped because task is stopping")
					return
				}
			}
		}
	}()
	return nil
}

func sendVerificationResult(ctx context.Context, out chan<- []byte, resp []byte) bool {
	select {
	case out <- resp:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *EvmEngine) vertify(in []byte) ([]byte, error) {
	// Auto-detect V2 vs V1 wire format.
	if IsV2WireFormat(in) {
		return e.verifyV2(in)
	}

	var bean state.EntireCode
	if err := json.Unmarshal(in, &bean); err != nil {
		simpleLog("unmarshal vertify input error,err=", err)
		return nil, err
	}

	if bean.Entire.Header == nil {
		return nil, errors.New("nil pointer found")
	}
	if bean.Entire.Header.Number == nil {
		return nil, errors.New("block number unavailable")
	}
	if bean.Entire.Snap == nil {
		return nil, errors.New("snapshot unavailable")
	}

	blockNumber := bean.Entire.Header.Number.Uint64()
	blockHash := bean.Entire.Header.Hash()
	simpleLog("start verify ", "blockNr", blockNumber)

	// Multi-producer dedup: if another producer's push of the same block
	// already produced a signed result, return that cached result instead
	// of re-running the EVM. The hook is nil for non-mobile callers.
	if skip, cached := callShouldSkip(blockNumber, blockHash); skip {
		simpleLog("vertify dedup hit", "blockNr", blockNumber)
		return cached, nil
	}

	var hash commTyp.Hash
	hasher := sha3.NewLegacyKeccak256()
	if err := state.EncodeBeforeState(hasher, bean.Entire.Snap.Items, bean.Codes); err != nil {
		return nil, err
	}
	hasher.(crypto.KeccakState).Read(hash[:])
	if bean.Entire.Header.MixDigest != hash {
		simpleLog("misMatch state hash", "want", bean.Entire.Header.MixDigest, "get", hash, "block", blockNumber)
		return nil, errors.New("state verify failed")
	}

	entirecode := state.EntireCode(bean)
	stateRoot, err := verify(e.ctx, &entirecode)
	if err != nil {
		simpleLog("verify failed", "err", err)
		return nil, err
	}

	res := AggSign{}
	copy(res.StateRoot[:], stateRoot[:])
	if pubkIfce, err := BlsPublicKey(e.PrivKey); err == nil {
		if pubkStr, ok := pubkIfce.(string); ok {
			pubkBytes, err := hex.DecodeString(pubkStr)
			if err == nil {
				copy(res.PublicKey[:], pubkBytes)
			}
		}
	}

	simpleLog("calculated stateRoot:", "stateRoot", hexutil.Encode(res.StateRoot[:]))

	res.Number = blockNumber
	sk, err := decodeSecretKey(e.PrivKey)
	if err != nil {
		return nil, err
	}

	copy(res.Sign[:], sk.Sign(res.StateRoot[:]).Marshal())

	simpleLog("sign stateRoot:", "Sign", hexutil.Encode(res.Sign[:]))

	res.Address = commTyp.HexToAddress(e.Account)

	resBytes, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}

	// Cache for subsequent producer pushes of the same block.
	callCache(blockNumber, blockHash, resBytes)

	return resBytes, nil
}

// globalCodeCache is a package-level code cache shared across V2 verifications.
// Mobile parallel verifiers benefit from cross-block code reuse — once a
// contract has been received in any packet, subsequent packets can omit it
// from their Bytecodes section.
var globalCodeCache = NewCodeCache(4096)

// verifyV2 handles V2 stream packet verification.
//
// Flow:
//  1. Decode the StreamPacket
//  2. Re-execute the block via ExecuteAndVerifyV2 (real EVM, real receipts)
//  3. If receipts root matches header, BLS-sign a VerificationReceipt
//  4. Marshal the receipt as JSON for the WebSocket transport
//
// On any failure (decode, execution, root mismatch) verifyV2 returns an
// error and does NOT produce a signed receipt — the IDC node will see no
// receipt for this block from this verifier, which is the correct
// failure-quiet behavior.
func (e *EvmEngine) verifyV2(data []byte) ([]byte, error) {
	pkt, err := DecodeStreamPacket(data)
	if err != nil {
		simpleLog("decode V2 stream packet error,err=", err)
		return nil, err
	}

	// Multi-producer dedup: same (number, hash) coming via a different
	// producer connection short-circuits to the cached signed result.
	// Header decode is needed for the block number; cheap relative to EVM.
	var hdrForDedup block.Header
	dedupReady := hdrForDedup.Unmarshal(pkt.HeaderRLP) == nil && hdrForDedup.Number != nil
	if dedupReady {
		num := hdrForDedup.Number.Uint64()
		if skip, cached := callShouldSkip(num, pkt.BlockHash); skip {
			simpleLog("verifyV2 dedup hit", "blockNr", num)
			return cached, nil
		}
	}

	result, err := ExecuteAndVerifyV2(pkt, globalCodeCache, nil)
	if err != nil {
		simpleLog("V2 re-execution failed", "err", err)
		return nil, err
	}

	simpleLog("V2 verify ok",
		"blockNr", result.BlockNumber,
		"txs", result.TxCount,
		"readLog", result.WitnessReadLogLen,
		"newCodes", result.UncachedBytecodes,
		"receiptsRoot", hexutil.Encode(result.ComputedReceiptsRoot[:]))

	sk, err := decodeSecretKey(e.PrivKey)
	if err != nil {
		return nil, err
	}

	receipt := VerificationReceipt{
		BlockHash:            result.BlockHash,
		BlockNumber:          result.BlockNumber,
		ComputedReceiptsRoot: result.ComputedReceiptsRoot,
		TimestampMs:          uint64(time.Now().UnixMilli()),
	}

	if pubkIfce, err := BlsPublicKey(e.PrivKey); err == nil {
		if pubkStr, ok := pubkIfce.(string); ok {
			if pubkBytes, derr := hex.DecodeString(pubkStr); derr == nil {
				copy(receipt.VerifierPubkey[:], pubkBytes)
			}
		}
	}

	copy(receipt.Signature[:], sk.Sign(receipt.SigningMessage()).Marshal())

	resBytes, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}

	// Cache for subsequent producer pushes of the same block.
	callCache(result.BlockNumber, result.BlockHash, resBytes)

	return resBytes, nil
}

func (e *EvmEngine) unwrapJSONRPC(in []byte) ([]byte, error) {
	req := new(jsonrpc.Request)
	if err := json.Unmarshal(in, req); err != nil {
		return nil, err
	}
	if len(req.Params) == 0 {
		return []byte{}, errors.New("empty request params")
	}

	type innerProtocol struct {
		Subscription string          `json:"subscription"`
		Result       json.RawMessage `json:"result"`
	}

	innerReq := new(innerProtocol)
	if err := json.Unmarshal(req.Params, innerReq); err != nil {
		return nil, err
	}

	return innerReq.Result, nil
}
