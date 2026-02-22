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

package jsonrpc_test

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// Block is a minimal block representation for subscription examples.
type Block struct {
	Number *hexutil.Big
}

// subscribeBlocks demonstrates maintaining a subscription for new blocks.
func subscribeBlocks(client *jsonrpc.Client, subch chan Block) {
	ctx := context.Background()

	sub, err := client.Subscribe(ctx, "eth", subch, "newHeads")
	if err != nil {
		fmt.Println("subscribe error:", err)
		return
	}

	fmt.Println("connection lost: ", <-sub.Err())
}
