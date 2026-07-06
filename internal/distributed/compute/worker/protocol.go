// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Wire protocol between RemoteExecutor and Worker: a versioned JSON envelope
// (slice-2 simplicity; the signature covers a canonical keccak digest, never
// the JSON bytes, so the encoding can change without breaking signatures).
//
// Every response — success or refusal — is signed by the worker's secp256k1
// key over digest = keccak(reqID ‖ worker ‖ keccak(output) ‖ keccak(errMsg)).
// The executor recovers the signer and requires it to equal the address it
// dispatched to: results and refusals are unforgeable and non-transplantable
// (bound to this request, this worker).

package worker

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/compute/scheduler"
)

const protocolVersion = 1

// wireSpec carries exactly the TaskSpec fields a worker needs to execute
// deterministically. Scheduling-side fields (prices, redundancy, windows)
// deliberately never cross the wire.
type wireSpec struct {
	ProgramHash types.Hash `json:"programHash"`
	Bytecode    []byte     `json:"bytecode"`
	FuncName    string     `json:"funcName"`
	Input       []byte     `json:"input"`
	FuelLimit   uint64     `json:"fuelLimit"`
	LeaseMs     int64      `json:"leaseMs"` // worker-side execution budget
}

func toWireSpec(s *scheduler.TaskSpec) wireSpec {
	return wireSpec{
		ProgramHash: s.ProgramHash,
		Bytecode:    s.Bytecode,
		FuncName:    s.FuncName,
		Input:       s.Input,
		FuelLimit:   s.FuelLimit,
		LeaseMs:     s.Lease.Milliseconds(),
	}
}

func (w wireSpec) toTaskSpec() *scheduler.TaskSpec {
	return &scheduler.TaskSpec{
		ProgramHash: w.ProgramHash,
		Bytecode:    w.Bytecode,
		FuncName:    w.FuncName,
		Input:       w.Input,
		FuelLimit:   w.FuelLimit,
	}
}

// taskRequest asks a worker to execute one attempt.
type taskRequest struct {
	Ver     int           `json:"ver"`
	ReqID   types.Hash    `json:"reqId"`
	ReplyTo types.Address `json:"replyTo"`
	Spec    wireSpec      `json:"spec"`
}

// taskResponse returns a signed output or a signed refusal.
type taskResponse struct {
	Ver    int           `json:"ver"`
	ReqID  types.Hash    `json:"reqId"`
	Worker types.Address `json:"worker"`
	Output []byte        `json:"output,omitempty"`
	ErrMsg string        `json:"errMsg,omitempty"`
	Sig    []byte        `json:"sig"`
}

// responseDigest is the canonical signing digest for a response.
func responseDigest(reqID types.Hash, worker types.Address, output []byte, errMsg string) types.Hash {
	outH := crypto.Keccak256Hash(output)
	errH := crypto.Keccak256Hash([]byte(errMsg))
	return crypto.Keccak256Hash(reqID[:], worker[:], outH[:], errH[:])
}

// signResponse fills resp.Sig.
func signResponse(resp *taskResponse, key *ecdsa.PrivateKey) error {
	digest := responseDigest(resp.ReqID, resp.Worker, resp.Output, resp.ErrMsg)
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return err
	}
	resp.Sig = sig
	return nil
}

// verifyResponse checks the signature and that the signer IS the worker the
// request was dispatched to.
func verifyResponse(resp *taskResponse, expectedWorker types.Address) error {
	if resp.Worker != expectedWorker {
		return fmt.Errorf("worker: response claims %s, dispatched to %s",
			resp.Worker.Hex(), expectedWorker.Hex())
	}
	digest := responseDigest(resp.ReqID, resp.Worker, resp.Output, resp.ErrMsg)
	pub, err := crypto.SigToPub(digest[:], resp.Sig)
	if err != nil {
		return fmt.Errorf("worker: bad signature: %w", err)
	}
	if got := crypto.PubkeyToAddress(*pub); got != expectedWorker {
		return fmt.Errorf("worker: signature by %s, want %s", got.Hex(), expectedWorker.Hex())
	}
	return nil
}

func encode(v any) ([]byte, error) { return json.Marshal(v) }
func decode(b []byte, v any) error { return json.Unmarshal(b, v) }
