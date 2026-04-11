// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Participation flags unit for the cltypes package.
// Declares the ParticipationFlags and ParticipationFlagsList type aliases.
// Exports helpers such as Add, HasFlag, Copy, and
// ParticipationFlagsListFromBytes.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/utils"
)

type ParticipationFlags byte

func (f ParticipationFlags) Add(index int) ParticipationFlags {
	return f | ParticipationFlags(utils.PowerOf2(uint64(index)))
}

func (f ParticipationFlags) HasFlag(index int) bool {
	flag := ParticipationFlags(utils.PowerOf2(uint64(index)))
	return f&flag == flag
}

type ParticipationFlagsList []ParticipationFlags

func (p ParticipationFlagsList) Bytes() []byte {
	b := make([]byte, len(p))
	for i := range p {
		b[i] = byte(p[i])
	}
	return b
}

func (p ParticipationFlagsList) Copy() ParticipationFlagsList {
	c := make(ParticipationFlagsList, len(p))
	copy(c, p)
	return c
}

func ParticipationFlagsListFromBytes(buf []byte) ParticipationFlagsList {
	flagsList := make([]ParticipationFlags, len(buf))
	for i := range flagsList {
		flagsList[i] = ParticipationFlags(buf[i])
	}
	return flagsList
}
