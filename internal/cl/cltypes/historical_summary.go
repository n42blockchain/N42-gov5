// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Historical summary unit for the cltypes package.
// Defines the HistoricalSummary types.
// Exports helpers such as EncodeSSZ, DecodeSSZ, HashSSZ, and
// EncodingSizeSSZ.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/length"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
)

type HistoricalSummary struct {
	BlockSummaryRoot common.Hash `json:"block_summary_root"`
	StateSummaryRoot common.Hash `json:"state_summary_root"`
}

func (h *HistoricalSummary) EncodeSSZ(buf []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(buf, h.BlockSummaryRoot[:], h.StateSummaryRoot[:])
}

func (h *HistoricalSummary) DecodeSSZ(buf []byte, _ int) error {
	return ssz2.UnmarshalSSZ(buf, 0, h.BlockSummaryRoot[:], h.StateSummaryRoot[:])
}

func (h *HistoricalSummary) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(h.BlockSummaryRoot[:], h.StateSummaryRoot[:])
}

func (*HistoricalSummary) EncodingSizeSSZ() int {
	return length.Hash * 2
}
