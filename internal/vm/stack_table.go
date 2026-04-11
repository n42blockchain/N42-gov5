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
//
// Stack limit helper shared by the jump table. maxStack computes an
// operation's maximum allowed pre-execution stack depth as
// params.StackLimit + numPop - numPush so that executing the opcode
// cannot push the stack past StackLimit. Every operation entry in the
// JumpTable populates its maxStack field through this function during
// validateAndFillMaxStack.

package vm

import (
	"github.com/n42blockchain/N42/params"
)

func maxStack(pop, push int) int {
	return int(params.StackLimit) + pop - push
}
