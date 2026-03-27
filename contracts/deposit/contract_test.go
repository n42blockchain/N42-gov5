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

package deposit

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/common/hexutil"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/params"
)

func TestBLS(t *testing.T) {
	sig, _ := hexutil.Decode("0xab22c6b63e3595630ffe8ed2903dfeba2a781c2d33dc66f88442982b65c5fcce9a8078f9ae419c95eecd2a5546a06e371196855311a930a4ad404321083f4f058c41e2d6c2e1f2bf11b1cd9b73d65a0a169a81cc1e60b50164aa7b322396be67")
	bp, _ := hexutil.Decode("0xa20699fa55487f79c1400e2be5bb6acf89b0c5880becfa4b0560b9994bd8050616886a8fb71bdf15065dd31dd2858c18")
	msg := new(uint256.Int).Mul(uint256.NewInt(params.N), uint256.NewInt(50)) //50 N
	signature, err := bls.SignatureFromBytes(sig)
	if err != nil {
		t.Fatal("cannot unpack BLS signature", err)
	}

	publicKey, err := bls.PublicKeyFromBytes(bp)
	if err != nil {
		t.Fatal("cannot unpack BLS publicKey", err)
	}

	if !signature.Verify(publicKey, msg.Bytes()) {
		t.Fatal("bls cannot verify signature")
	}
}

func TestUint256(t *testing.T) {
	n50Hex, _ := hexutil.Decode("0x2B5E3AF16B1880000") // 50 N
	n50Uint256 := new(uint256.Int).Mul(uint256.NewInt(params.N), uint256.NewInt(50))
	t.Logf("50 N uint256 bytes:%s, hex Bytes: %s", hexutil.Encode(n50Uint256.Bytes()), hexutil.Encode(n50Hex))

	n500Hex, _ := hexutil.Decode("0x1B1AE4D6E2EF500000") // 500 N
	n500Uint256 := new(uint256.Int).Mul(uint256.NewInt(params.N), uint256.NewInt(500))
	t.Logf("500 N uint256 bytes:%s, hex Bytes: %s", hexutil.Encode(n500Uint256.Bytes()), hexutil.Encode(n500Hex))

	n100Hex, _ := hexutil.Decode("0x56BC75E2D63100000") // 100 N
	n100Uint256 := new(uint256.Int).Mul(uint256.NewInt(params.N), uint256.NewInt(100))
	t.Logf("100 N uint256 bytes:%s, hex Bytes: %s", hexutil.Encode(n100Uint256.Bytes()), hexutil.Encode(n100Hex))
}

func TestNewDeposit_GlobalEventClosed(t *testing.T) {
	event.GlobalEvent = event.Event{}
	t.Cleanup(func() {
		event.GlobalEvent = event.Event{}
	})

	newLogsCh := make(chan common.NewLogsEvent, 1)
	removedLogsCh := make(chan common.RemovedLogsEvent, 1)
	if _, err := event.GlobalEvent.Subscribe(newLogsCh); err != nil {
		t.Fatal(err)
	}
	if _, err := event.GlobalEvent.Subscribe(removedLogsCh); err != nil {
		t.Fatal(err)
	}
	event.GlobalEvent.Close()

	d := NewDeposit(context.Background(), nil, nil, nil)
	if d.logsSub != nil || d.rmLogsSub != nil {
		t.Fatal("expected closed event scopes to disable deposit subscriptions")
	}

	d.Start()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := d.Stop(); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop() blocked with disabled subscriptions")
	}
}
