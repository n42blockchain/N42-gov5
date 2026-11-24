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

package block

import (
	"github.com/n42blockchain/N42/lib/rlp"
)

// headerRLP has Header's fields and tags but none of its methods, so the
// reflection decoder can fill it without recursing into Header.DecodeRLP.
type headerRLP Header

// RLP list positions (0-based) of the optional integer header elements whose
// present-zero value encodes as the empty string, the same bytes as the
// placeholder written for a nil gap before a later present field.
const (
	rlpPosBlobGasUsed   = 17
	rlpPosExcessBlobGas = 18
	rlpPosSlotNumber    = 22
)

// DecodeRLP decodes the header and then restores present-zero integer
// optionals that the rlp:"optional,nil" tags turn into nil.
//
// The tags are needed so that a nil gap placeholder (an empty string written
// for an absent optional that precedes a present one) decodes back to nil.
// For an integer field, however, an encoded zero is the same empty string, so
// the tag also turned a present zero into nil: a Prague header with
// BlobGasUsed = ExcessBlobGas = 0 arrived with both fields absent. The hash is
// unaffected (the encoder writes the same bytes for both), but every check on
// the field's presence (profile 1 B9) failed on the receiving side.
//
// The bytes cannot tell a gap from a zero, so the header's own shape decides:
//   - the last element on the wire is never a gap (the encoder stops at the
//     last present field), so an empty last element is a present zero;
//   - a present RequestsHash (Prague) or SlotNumber (Amsterdam) implies Cancun,
//     and so do the blob fields themselves: in such a header an empty
//     BlobGasUsed / ExcessBlobGas element is a present zero.
//
// ParentBeaconRoot is deliberately not a Cancun witness: the HotStuff
// committee-evidence link stamps it on pre-Cancun headers, whose blob fields
// are real gaps. SlotNumber keeps its gap reading when MobileRegistryRoot
// follows it (pre-Amsterdam native headers); the only present zero slot with a
// later field is the genesis header, which is never decoded from the wire.
// BaseFee needs no repair (its decoder already yields a zero) and hash-typed
// optionals need none either: a present value is always 32 bytes, so only a
// gap can be empty.
func (h *Header) DecodeRLP(s *rlp.Stream) error {
	raw, err := s.Raw()
	if err != nil {
		return err
	}
	content, _, err := rlp.SplitList(raw)
	if err != nil {
		return err
	}
	n, err := rlp.CountValues(content)
	if err != nil {
		return err
	}
	if err := rlp.DecodeBytes(raw, (*headerRLP)(h)); err != nil {
		return err
	}
	h.restorePresentZeros(n)
	return nil
}

// restorePresentZeros applies the DecodeRLP rules to a header decoded from an
// RLP list of n elements.
func (h *Header) restorePresentZeros(n int) {
	last := n - 1
	cancun := h.RequestsHash != nil || h.SlotNumber != nil ||
		h.BlobGasUsed != nil || h.ExcessBlobGas != nil
	if h.ExcessBlobGas == nil && rlpPosExcessBlobGas < n && (cancun || last == rlpPosExcessBlobGas) {
		h.ExcessBlobGas = new(uint64)
	}
	// ExcessBlobGas is now present whenever this header is Cancun, so it also
	// covers a BlobGasUsed that precedes a trailing present-zero ExcessBlobGas.
	if h.BlobGasUsed == nil && rlpPosBlobGasUsed < n && (h.ExcessBlobGas != nil || last == rlpPosBlobGasUsed) {
		h.BlobGasUsed = new(uint64)
	}
	if h.SlotNumber == nil && last == rlpPosSlotNumber {
		h.SlotNumber = new(uint64)
	}
}
