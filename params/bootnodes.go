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
// Bootstrap node lists for the N42 P2P discovery layer.
// Exposes MainnetBootnodes and TestnetBootnodes as enode URL slices
// used by devp2p discovery to seed the initial peer table. Each entry
// is a signed ENR embedding secp256k1 pubkey, IP and UDP port.

package params

// MainnetBootnodes are the enode URLs of the P2P bootstrap nodes running on
// the main N42 network.
var MainnetBootnodes = []string{
	"enr:-Je4QGJb6IZbaceKodV55AtTd9oxwiqeVeZ1LwHA9MId-k8XAsKAAyxTi_Pf_nkRTuH-vQnICSLg2Uu_PmG3Vwx8eaIChmFzdEVucoQThzS3gmlkgnY0gmlwhAWhUVqJc2VjcDI1NmsxoQICbV6ssdll4ktSNrWY2FaYTFjio7WqVJLSPNwJMv2SBYN0Y3CCJ2aDdWRwgidl",
}

// TestnetBootnodes are the enode URLs of the P2P bootstrap nodes running on
// the testnet N42 network.
var TestnetBootnodes = []string{
	"enr:-Je4QNvwxHKua9TMkdz38rdpxh0-haLEJn8-wAM9UqdUG_oHPbZpoNq9e-aPw7y58GvFMjQ0Pl-AUd1sGGLE_gTlZRcChmFzdEVucoS8iGe2gmlkgnY0gmlwhAWh_DuJc2VjcDI1NmsxoQJXwjUBMWh3C3YTD-UW_M2Df1piKwkCal8z7WahqX4QOYN0Y3CCJ2aDdWRwgidl", //
}

// EthereumMainnetBootnodes are the enode URLs of bootstrap nodes on the
// Ethereum mainnet devp2p (eth-1) network. Used by cmd/eth-el when running
// in "EL-only catch-up" mode where the local node fetches mainnet EL blocks
// directly from geth/erigon peers on port 30303, bypassing a consensus-
// layer client entirely.
//
// Source: https://github.com/ethereum/go-ethereum/blob/master/params/bootnodes.go
// (Ethereum Foundation maintained list, refreshed alongside geth releases.)
var EthereumMainnetBootnodes = []string{
	// Ethereum Foundation Go Bootnodes
	"enode://d860a01f9722d78051619d1e2351aba3f43f943f6f00718d1b9baa4101932a1f5011f16bb2b1bb35db20d6fe28fa0bf09636d26a87d31de9ec6203eeedb1f666@18.138.108.67:30303",   // bootnode-aws-ap-southeast-1-001
	"enode://22a8232c3abc76a16ae9d6c3b164f98775fe226f0917b0ca871128a74a8e9630b458460865bab457221f1d448dd9791d24c4e5d88786180ac185df813a68d4de@3.209.45.79:30303",      // bootnode-aws-us-east-1-001
	"enode://2b252ab6a1d0f971d9722cb839a42cb81db019ba44c08754628ab4a823487071b5695317c8ccd085219c3a03af063495b2f1da8d18218da2d6a82981b45e6ffc@65.108.70.101:30303", // bootnode-hetzner-hel
	"enode://4aeb4ab6c14b23e2c4cfdce879c04b0748a20d8e9b59e25ded2a08143e265c6c25936e74cbc8e641e3312ca288673d91f2f93f8e277de3cfa444ecdaaf982052@157.90.35.166:30303", // bootnode-hetzner-fsn
	// Legacy bootnode preserved for redundancy (may be offline; not in geth/erigon current list).
	"enode://a3435a0155a3e837c02f5e7f5662a2f1fbc25b48e4dc232016e1c51b544cb5b4510ef633ea3278c0e970fa8ad8141e2d4d0f9f95456c537ff05fdf9b31c15072@165.232.74.108:30303",
}
