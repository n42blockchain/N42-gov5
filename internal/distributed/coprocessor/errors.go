// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package coprocessor

import "errors"

var (
	ErrProgramNotRegistered = errors.New("coprocessor: program not registered")
	ErrTaskNotFound         = errors.New("coprocessor: task not found")
	ErrTaskNotPending       = errors.New("coprocessor: task not in pending/proving state")
	ErrInvalidProof         = errors.New("coprocessor: invalid proof data")
	ErrMaxPendingReached    = errors.New("coprocessor: max pending tasks reached")
)
