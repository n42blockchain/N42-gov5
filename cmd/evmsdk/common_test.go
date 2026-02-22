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

func TestBenchmark(t *testing.T) {
	key, _ := crypto.HexToECDSA("d6d8d19bd786d6676819b806694b1100a4414a94e51e9a82a351bd8f7f3f3658")
	addr := crypto.PubkeyToAddress(key.PublicKey)
	fmt.Println(addr)
}
