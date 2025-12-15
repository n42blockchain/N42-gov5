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
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-kit/kit/transport/http/jsonrpc"
	"github.com/gorilla/websocket"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/crypto/bls"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// Environment variable names for configuration
const (
	EnvPrivateKey  = "N42_VERIFY_PRIVATE_KEY"
	EnvWebSocketURL = "N42_VERIFY_WS_URL"
	DefaultWSURL   = "ws://127.0.0.1:20013"
)

var privateKey bls.SecretKey
var addressKey types.Address

func RootContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()

		ch := make(chan os.Signal, 1)
		defer close(ch)

		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(ch)

		select {
		case sig := <-ch:
			log.Info("Got interrupt, shutting down...", "sig", sig)
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func main() {
	// SECURITY: Read private key from environment variable instead of hardcoding
	privateKeyHex := os.Getenv(EnvPrivateKey)
	if privateKeyHex == "" {
		log.Error("Private key not set. Please set environment variable: " + EnvPrivateKey)
		os.Exit(1)
	}

	var err error
	sByte, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		log.Error("Failed to decode private key", "error", err)
		os.Exit(1)
	}

	if len(sByte) != 32 {
		log.Error("Invalid private key length, expected 32 bytes")
		os.Exit(1)
	}

	var sb [32]byte
	copy(sb[:], sByte)
	privateKey, err = bls.SecretKeyFromRandom32Byte(sb)
	if err != nil {
		log.Error("Failed to create BLS secret key", "error", err)
		os.Exit(1)
	}

	ecdPk, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		log.Error("Failed to create ECDSA private key", "error", err)
		os.Exit(1)
	}
	addressKey = crypto.PubkeyToAddress(ecdPk.PublicKey)

	ctx, cancel := RootContext()
	defer cancel()

	// Get WebSocket URL from environment or use default
	wsURL := os.Getenv(EnvWebSocketURL)
	if wsURL == "" {
		wsURL = DefaultWSURL
	}

	con, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		log.Error("Failed to connect to WebSocket", "url", wsURL, "error", err)
		os.Exit(1)
	}
	defer con.Close()

	end := make(chan struct{})
	defer close(end)

	go func() {
		for {
			select {
			case <-ctx.Done():
				end <- struct{}{}
				return
			default:
				typ, msg, err := con.ReadMessage()
				if nil != err {
					log.Errorf("read msg failed: %v", err)
					continue
				}
				if typ == websocket.TextMessage {
					fmt.Println("read msg: ", string(msg))
					params, err := unwrapJSONRPC(msg)
					if nil != err {
						log.Warn(err.Error())
						continue
					}

					bean := new(state.EntireCode)
					if err := json.Unmarshal(params, bean); err != nil {
						log.Errorf("unmarshal entire failed, %v", err)
						continue
					}

					root := verify(ctx, bean)
					res := api.AggSign{}
					res.Number = bean.Entire.Header.Number.Uint64()
					res.Address = addressKey
					res.StateRoot = root
					copy(res.Sign[:], privateKey.Sign(root[:]).Marshal())
					in, err := json.Marshal(res)
					if err != nil {
						log.Error("Failed to marshal response", "error", err)
						continue
					}

					wrapRequest, _ := wrapJSONRPCRequest(in)
					if err := con.WriteMessage(websocket.TextMessage, wrapRequest); nil != err {
						log.Error("write msg failed: ", err)
					}
					log.Infof("write msg: %s", wrapRequest)
				}
			}
		}
	}()

	if err = con.PingHandler()(""); err != nil {
		log.Error("Ping failed", "error", err)
		os.Exit(1)
	}

	subscribeMsg := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"method": "eth_subscribe",
		"params": [
		  "minedBlock",
		  "%s"
		],
		"id": 1
	  }`, addressKey.String())

	if err := con.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		log.Error("Failed to subscribe", "error", err)
		cancel()
	}

	<-end
}

func unwrapJSONRPC(in []byte) ([]byte, error) {
	//"{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32000,\"message\":\"unauthed address: 0xeB156a42dcaFcf155B07f3638892440C7dE5d564\"}}\n"
	//ws consumer received msg:%!(EXTRA string=ws consumer received msg:, string={"jsonrpc":"2.0","id":1,"result":"0x96410b68a9f8875bb20fde06823eb861"}
	req := new(jsonrpc.Request)
	if err := json.Unmarshal(in, req); err != nil {
		return nil, err
	}
	if len(req.Params) == 0 {
		return []byte{}, errors.New("empty request params")
	}

	//type innerProtocolEntire struct {
	//	Entire json.RawMessage `json:"Entire"`
	//}
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

type JSONRPCRequest struct {
	JsonRpc string            `json:"jsonrpc"`
	Method  string            `json:"method"`
	ID      int               `json:"id"`
	Params  []json.RawMessage `json:"params"`
}

func wrapJSONRPCRequest(in []byte) ([]byte, error) {
	d := &JSONRPCRequest{
		JsonRpc: "2.0",
		Method:  "eth_submitSign",
		ID:      1,
		Params:  make([]json.RawMessage, 1),
	}
	d.Params[0] = in
	return json.Marshal(d)
}
