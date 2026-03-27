package vm

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

type legacyPrecompileSet string

const (
	legacyPrecompileSetHomestead      legacyPrecompileSet = "homestead"
	legacyPrecompileSetByzantium      legacyPrecompileSet = "byzantium"
	legacyPrecompileSetIstanbul       legacyPrecompileSet = "istanbul"
	legacyPrecompileSetIstanbulForBSC legacyPrecompileSet = "istanbul_bsc"
	legacyPrecompileSetBerlin         legacyPrecompileSet = "berlin"
	legacyPrecompileSetCancun         legacyPrecompileSet = "cancun"
	legacyPrecompileSetPrague         legacyPrecompileSet = "prague"
	legacyPrecompileSetPectra         legacyPrecompileSet = "pectra"
	legacyPrecompileSetOsaka          legacyPrecompileSet = "osaka"
	legacyPrecompileSetFusaka         legacyPrecompileSet = "fusaka"
	legacyPrecompileSetNano           legacyPrecompileSet = "nano"
	legacyPrecompileSetMoran          legacyPrecompileSet = "moran"
)

func activeLegacyPrecompileSet(rules *params.Rules) legacyPrecompileSet {
	if rules == nil {
		return legacyPrecompileSetHomestead
	}
	switch {
	case rules.IsMoran:
		return legacyPrecompileSetMoran
	case rules.IsNano:
		return legacyPrecompileSetNano
	case rules.IsFusaka:
		return legacyPrecompileSetFusaka
	case rules.IsOsaka:
		return legacyPrecompileSetOsaka
	case rules.IsPectra:
		return legacyPrecompileSetPectra
	case rules.IsPrague:
		return legacyPrecompileSetPrague
	case rules.IsCancun:
		return legacyPrecompileSetCancun
	case rules.IsBerlin:
		return legacyPrecompileSetBerlin
	case rules.IsIstanbul:
		if rules.IsParlia {
			return legacyPrecompileSetIstanbulForBSC
		}
		return legacyPrecompileSetIstanbul
	case rules.IsByzantium:
		return legacyPrecompileSetByzantium
	default:
		return legacyPrecompileSetHomestead
	}
}

func legacyPrecompileContractsBySet(set legacyPrecompileSet) map[types.Address]PrecompiledContract {
	switch set {
	case legacyPrecompileSetMoran:
		return PrecompiledContractsIsMoran
	case legacyPrecompileSetNano:
		return PrecompiledContractsNano
	case legacyPrecompileSetFusaka:
		return PrecompiledContractsFusaka
	case legacyPrecompileSetOsaka:
		return PrecompiledContractsOsaka
	case legacyPrecompileSetPectra:
		return PrecompiledContractsPectra
	case legacyPrecompileSetPrague:
		return PrecompiledContractsPrague
	case legacyPrecompileSetCancun:
		return PrecompiledContractsCancun
	case legacyPrecompileSetBerlin:
		return PrecompiledContractsBerlin
	case legacyPrecompileSetIstanbulForBSC:
		return PrecompiledContractsIstanbulForBSC
	case legacyPrecompileSetIstanbul:
		return PrecompiledContractsIstanbul
	case legacyPrecompileSetByzantium:
		return PrecompiledContractsByzantium
	default:
		return PrecompiledContractsHomestead
	}
}

func legacyPrecompileAddressesBySet(set legacyPrecompileSet) []types.Address {
	switch set {
	case legacyPrecompileSetMoran:
		return PrecompiledAddressesMoran
	case legacyPrecompileSetNano:
		return PrecompiledAddressesNano
	case legacyPrecompileSetFusaka:
		return PrecompiledAddressesFusaka
	case legacyPrecompileSetOsaka:
		return PrecompiledAddressesOsaka
	case legacyPrecompileSetPectra:
		return PrecompiledAddressesPectra
	case legacyPrecompileSetPrague:
		return PrecompiledAddressesPrague
	case legacyPrecompileSetCancun:
		return PrecompiledAddressesCancun
	case legacyPrecompileSetBerlin:
		return PrecompiledAddressesBerlin
	case legacyPrecompileSetIstanbulForBSC:
		return PrecompiledAddressesIstanbulForBSC
	case legacyPrecompileSetIstanbul:
		return PrecompiledAddressesIstanbul
	case legacyPrecompileSetByzantium:
		return PrecompiledAddressesByzantium
	default:
		return PrecompiledAddressesHomestead
	}
}
