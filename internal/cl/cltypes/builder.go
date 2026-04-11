// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Builder unit for the cltypes package.
// Defines the ValidatorRegistration and ValidatorRegistrationMessage types.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import "github.com/n42blockchain/N42/internal/cl/depshim/common"

// ValidatorRegistration is used as request payload for validator registration in builder client.
type ValidatorRegistration struct {
	Message   ValidatorRegistrationMessage `json:"message"`
	Signature common.Bytes96               `json:"signature"`
}

type ValidatorRegistrationMessage struct {
	FeeRecipient common.Address `json:"fee_recipient"`
	GasLimit     string         `json:"gas_limit"`
	Timestamp    string         `json:"timestamp"`
	PubKey       common.Bytes48 `json:"pubkey"`
}
