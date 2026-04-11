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
// Sentinel errors shared by every accounts backend.
// Exposes ErrUnknownAccount, ErrUnknownWallet, ErrNotSupported,
// ErrInvalidPassphrase, ErrWalletAlreadyOpen and ErrWalletClosed so
// callers can use errors.Is for fine-grained dispatch. Declares
// AuthNeededError plus NewAuthNeededError, signalling that a backend
// (e.g. hardware wallet, clef, keystore) needs a passphrase, PIN or
// out-of-band confirmation before a sign operation can succeed.

package accounts

import (
	"errors"
	"fmt"
)

var (
	// ErrUnknownAccount is returned for any requested operation for which no backend
	// provides the specified account.
	ErrUnknownAccount = errors.New("unknown account")

	// ErrUnknownWallet is returned for any requested operation for which no backend
	// provides the specified wallet.
	ErrUnknownWallet = errors.New("unknown wallet")

	// ErrNotSupported is returned when an operation is requested from an account
	// backend that it does not support.
	ErrNotSupported = errors.New("not supported")

	// ErrInvalidPassphrase is returned when a decryption operation receives a bad
	// passphrase.
	ErrInvalidPassphrase = errors.New("invalid password")

	// ErrWalletAlreadyOpen is returned if a wallet is attempted to be opened the
	// second time.
	ErrWalletAlreadyOpen = errors.New("wallet already open")

	// ErrWalletClosed is returned if a wallet is offline.
	ErrWalletClosed = errors.New("wallet closed")
)

// AuthNeededError is returned by backends for signing requests where the user
// is required to provide further authentication before signing can succeed.
//
// This usually means either that a password needs to be supplied, or perhaps a
// one time PIN code displayed by some hardware device.
type AuthNeededError struct {
	Needed string // Extra authentication the user needs to provide
}

// NewAuthNeededError creates a new authentication error with the extra details
// about the needed fields set.
func NewAuthNeededError(needed string) error {
	return &AuthNeededError{
		Needed: needed,
	}
}

// Error implements the standard error interface.
func (err *AuthNeededError) Error() string {
	return fmt.Sprintf("authentication needed: %s", err.Needed)
}
