// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package evmsdk

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"

	"github.com/go-kit/kit/transport/http/jsonrpc"

	"github.com/n42blockchain/N42/crypto"
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
	simpleLog("start verify ", "blockNr", blockNumber)

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

	return resBytes, nil
}

// globalCodeCache is a package-level code cache shared across V2 verifications.
var globalCodeCache = NewCodeCache(4096)

// verifyV2 handles V2 stream packet verification.
// Called from vertify() when V2 wire format is detected.
func (e *EvmEngine) verifyV2(data []byte) ([]byte, error) {
	pkt, err := DecodeStreamPacket(data)
	if err != nil {
		simpleLog("decode V2 stream packet error,err=", err)
		return nil, err
	}

	blockNumber, err := pkt.HeaderInfo()
	if err != nil {
		simpleLog("extract V2 header info error,err=", err)
		return nil, err
	}

	simpleLog("start V2 verify", "blockNr", blockNumber)

	// Update code cache with received bytecodes.
	for _, entry := range pkt.Bytecodes {
		globalCodeCache.Insert(entry.Hash, entry.Code)
	}

	// Build the AggSign result (same format as V1 for backward compat).
	res := AggSign{}
	res.Number = blockNumber
	copy(res.StateRoot[:], pkt.BlockHash[:])

	if pubkIfce, err := BlsPublicKey(e.PrivKey); err == nil {
		if pubkStr, ok := pubkIfce.(string); ok {
			pubkBytes, err := hex.DecodeString(pubkStr)
			if err == nil {
				copy(res.PublicKey[:], pubkBytes)
			}
		}
	}

	simpleLog("V2 stateRoot:", "stateRoot", hexutil.Encode(res.StateRoot[:]))

	sk, err := decodeSecretKey(e.PrivKey)
	if err != nil {
		return nil, err
	}

	copy(res.Sign[:], sk.Sign(res.StateRoot[:]).Marshal())

	simpleLog("V2 sign stateRoot:", "Sign", hexutil.Encode(res.Sign[:]))

	res.Address = commTyp.HexToAddress(e.Account)

	resBytes, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}

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
