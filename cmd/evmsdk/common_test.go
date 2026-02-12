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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/crypto"
)

func TestGetNetInfos(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetNetInfos(); got != tt.want {
				t.Errorf("GetNetInfos() = %v, want %v", got, tt.want)
			} else {
				t.Log(got)
				fmt.Println(got)
			}
		})
	}
}

func TestBlsSign(t *testing.T) {
	pk := make([]byte, 32)
	_, err := rand.Read(pk)
	if err != nil {
		t.Error(err)
	}

	req := `
{
	"type":"blssign",
	"val":{
		"priv_key":"` + hex.EncodeToString(pk) + `",
		"msg":"123123"
	}
}
	`
	resp := Emit(req)
	_ = resp
	fmt.Println(resp)
}

/*
0xb1fd8daE392D8E7c5aA0c8D405EFC56e1F55c42c
732d6346fc020087ac84bd1a42adb444e0f783fe907759ba7fa977aab3bf66fc
*/
func TestBlssign(t *testing.T) {
	resp := Emit(`
{"type": "blssign","val": {"priv_key": "202cbf36864a88e348c8f573aa0bc79f5a7119e58251c3580bb08af70cb2dfed", "msg" : "8ac7230489e80000"}}
	`)
	_ = resp
}

func TestEngineStop(t *testing.T) {
	if err := EE.Stop(); err != nil {
		t.Error(err)
	}
}

/*
{
    "jsonrpc": "2.0",
    "method": "eth_subscription",
    "params": {
        "subscription": "0x7e6684b10d6c906c47253690b8e9c7fa",
        "result": {
            "Entire": {
                "entire": {
                    "header": {
                        "parentHash": "0x76b286a66229e34455f5238b9f9c0d516903c222b4e5874a6662820e25dcde98",
                        "sha3Uncles": "0x0000000000000000000000000000000000000000000000000000000000000000",
                        "miner": "0x0000000000000000000000000000000000000000",
                        "stateRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
                        "transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
                        "receiptsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
                        "logsBloom": "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
                        "difficulty": "0x2",
                        "number": "0xd2",
                        "gasLimit": "0x18462971",
                        "gasUsed": "0x0",
                        "timestamp": "0x63a15be4",
                        "extraData": "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
                        "mixHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
                        "nonce": "0x0000000000000000",
                        "baseFeePerGas": null,
                        "hash": "0x051510ec6c9998e61126d285169ed6b24114b27c264ce0e5dad5e0a7bbac2f42"
                    },
                    "uncles": null,
                    "transactions": [],
                    "snap": {
                        "items": [
                            {
                                "key": "uelEd/X4i16Noul+hQbW5PzwTls=",
                                "value": "CAEaEzB4NjgxNTVhNDM2NzZlMDAwMDAiIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAKiDF0kYBhvcjPJJ+fbLcxwPA5QC2U8qCJzt7+tgEXYWkcA=="
                            },
                            {
                                "key": "m6M2g1Qiuu/FN9dWQpWdKoZlAKM=",
                                "value": "CAEaEzB4NjgxNTVhNDM2NzZlMDAwMDAiIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAKiDF0kYBhvcjPJJ+fbLcxwPA5QC2U8qCJzt7+tgEXYWkcA=="
                            },
                            {
                                "key": "RUH8HMtOBCo7rf5GkE+dItEntoI=",
                                "value": "CAEaEzB4NjgxNTVhNDM2NzZlMDAwMDAiIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAKiDF0kYBhvcjPJJ+fbLcxwPA5QC2U8qCJzt7+tgEXYWkcA=="
                            }
                        ],
                        "outHash": "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
                    },
                    "proof": "0x0000000000000000000000000000000000000000000000000000000000000000",
                    "senders": null
                },
                "codes": [],
                "headers": [],
                "rewards": {
                    "0x4541fc1ccb4e042a3badfe46904f9d22d127b682": {
                        "Int": "0x20f5b1eaad8d80000"
                    },
                    "0x9ba336835422baefc537d75642959d2a866500a3": {
                        "Int": "0x1d7d843dc3b480000"
                    },
                    "0xb9e94477f5f88b5e8da2e97e8506d6e4fcf04e5b": {
                        "Int": "0x1bc16d674ec800000"
                    }
                }
            }
        }
    }
}
*/

func TestBeanchMark(t *testing.T) {

	var (
		key, _ = crypto.HexToECDSA("d6d8d19bd786d6676819b806694b1100a4414a94e51e9a82a351bd8f7f3f3658")
		addr   = crypto.PubkeyToAddress(key.PublicKey)
		//signer  = new(types.HomesteadSigner)
		//content = context.Background()
	)

	fmt.Println(addr)
}
