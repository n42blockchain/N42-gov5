// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Version unit for the clparams package.
// Declares the StateVersion type aliases.
// Exports helpers such as Before, After, Equal, and BeforeOrEqual.
// Consensus-layer chain parameters and runtime configuration.

//go:build n42el

package clparams

import "fmt"

type StateVersion uint8

const (
	Phase0Version    StateVersion = 0
	AltairVersion    StateVersion = 1
	BellatrixVersion StateVersion = 2
	CapellaVersion   StateVersion = 3
	DenebVersion     StateVersion = 4
	ElectraVersion   StateVersion = 5
	FuluVersion      StateVersion = 6
	GloasVersion     StateVersion = 7
)

func (v StateVersion) String() string {
	switch v {
	case Phase0Version:
		return "phase0"
	case AltairVersion:
		return "altair"
	case BellatrixVersion:
		return "bellatrix"
	case CapellaVersion:
		return "capella"
	case DenebVersion:
		return "deneb"
	case ElectraVersion:
		return "electra"
	case FuluVersion:
		return "fulu"
	case GloasVersion:
		return "gloas"
	default:
		panic("unsupported fork version")
	}
}

func (v StateVersion) Before(other StateVersion) bool {
	return v < other
}

func (v StateVersion) After(other StateVersion) bool {
	return v > other
}

func (v StateVersion) Equal(other StateVersion) bool {
	return v == other
}

func (v StateVersion) BeforeOrEqual(other StateVersion) bool {
	return v <= other
}

func (v StateVersion) AfterOrEqual(other StateVersion) bool {
	return v >= other
}

// stringToClVersion converts the string to the current state version.
func StringToClVersion(s string) (StateVersion, error) {
	switch s {
	case "phase0":
		return Phase0Version, nil
	case "altair":
		return AltairVersion, nil
	case "bellatrix":
		return BellatrixVersion, nil
	case "capella":
		return CapellaVersion, nil
	case "deneb":
		return DenebVersion, nil
	case "electra":
		return ElectraVersion, nil
	case "fulu":
		return FuluVersion, nil
	case "gloas":
		return GloasVersion, nil
	default:
		return 0, fmt.Errorf("unsupported fork version %s", s)
	}
}

func ClVersionToString(s StateVersion) string {
	return s.String()
}
