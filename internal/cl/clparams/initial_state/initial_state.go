// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package initial_state

import (
	_ "embed"
	"fmt"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	chainspec "github.com/n42blockchain/N42/internal/cl/depshim/chainspec"
)

func downloadGenesisState(url string) ([]byte, error) {
	// Download genesis state by wget the url. MUST NOT RETURN NIL thorugh GET request. use go stnadard library
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download genesis state: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)

}

//go:embed mainnet.state.ssz
var mainnetStateSSZ []byte

//go:embed sepolia.state.ssz
var sepoliaStateSSZ []byte

//go:embed gnosis.state.ssz
var gnosisStateSSZ []byte

//go:embed chiado.state.ssz
var chiadoStateSSZ []byte

//go:embed bloatnet.state.ssz
var bloatnetStateSSZ []byte

// Return genesis state
func GetGenesisState(network clparams.NetworkType) (*state.CachingBeaconState, error) {
	_, config := clparams.GetConfigsByNetwork(network)
	returnState := state.New(config)

	switch network {
	case chainspec.MainnetChainID:
		if err := returnState.DecodeSSZ(mainnetStateSSZ, int(clparams.Phase0Version)); err != nil {
			return nil, err
		}
	case chainspec.SepoliaChainID:
		if err := returnState.DecodeSSZ(sepoliaStateSSZ, int(clparams.Phase0Version)); err != nil {
			return nil, err
		}
	case chainspec.GnosisChainID:
		if err := returnState.DecodeSSZ(gnosisStateSSZ, int(clparams.Phase0Version)); err != nil {
			return nil, err
		}
	case chainspec.ChiadoChainID:
		if err := returnState.DecodeSSZ(chiadoStateSSZ, int(clparams.Phase0Version)); err != nil {
			return nil, err
		}
	case chainspec.HoodiChainID:
		// Download genesis state by wget the url
		encodedState, err := downloadGenesisState("https://github.com/eth-clients/hoodi/raw/main/metadata/genesis.ssz")
		if err != nil {
			return nil, err
		}
		if err := returnState.DecodeSSZ(encodedState, int(clparams.DenebVersion)); err != nil {
			return nil, err
		}
	case chainspec.BloatnetNetworkID:
		if err := returnState.DecodeSSZ(bloatnetStateSSZ, int(clparams.ElectraVersion)); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	return returnState, nil
}

func IsGenesisStateSupported(network clparams.NetworkType) bool {
	return network == chainspec.MainnetChainID ||
		network == chainspec.SepoliaChainID ||
		network == chainspec.GnosisChainID ||
		network == chainspec.ChiadoChainID ||
		network == chainspec.HoodiChainID ||
		network == chainspec.BloatnetNetworkID
}
