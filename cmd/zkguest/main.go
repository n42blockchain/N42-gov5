//go:build !cgo

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

// cmd/zkguest is the RISC-V64 guest program for ZISK zkVM proof generation.
// It reads GuestInput from stdin, re-executes EVM transactions using a
// witness-backed state reader, and writes GuestOutput to stdout.
//
// Build: CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build -tags nosqlite,noboltdb,zkguest ./cmd/zkguest
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/n42blockchain/N42/internal/zkprover/guest"
)

func main() {
	inputData, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal("failed to read input: %v", err)
	}

	input, err := guest.DecodeGuestInput(inputData)
	if err != nil {
		fatal("failed to decode input: %v", err)
	}

	output := guest.Execute(input)

	outputData, err := guest.EncodeGuestOutput(output)
	if err != nil {
		fatal("failed to encode output: %v", err)
	}

	if _, err := os.Stdout.Write(outputData); err != nil {
		fatal("failed to write output: %v", err)
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
