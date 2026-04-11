// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package state

import "github.com/n42blockchain/N42/internal/cl/abstract"

func IncreaseBalance(b abstract.BeaconState, index, delta uint64) error {
	currentBalance, err := b.ValidatorBalance(int(index))
	if err != nil {
		return err
	}
	return b.SetValidatorBalance(int(index), currentBalance+delta)
}

func DecreaseBalance(b abstract.BeaconState, index, delta uint64) error {
	currentBalance, err := b.ValidatorBalance(int(index))
	if err != nil {
		return err
	}
	var newBalance uint64
	if currentBalance >= delta {
		newBalance = currentBalance - delta
	}
	return b.SetValidatorBalance(int(index), newBalance)
}
