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
package vm

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/blake2b"
	"github.com/n42blockchain/N42/crypto/bn256"
	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
	"golang.org/x/crypto/ripemd160"
)

// PrecompiledContract is the basic interface for native Go contracts. The implementation
// requires a deterministic gas count based on the input size of the Run method of the
// contract.
type PrecompiledContract interface {
	RequiredGas(input []byte) uint64  // RequiredPrice calculates the contract gas use
	Run(input []byte) ([]byte, error) // Run runs the precompiled contract
}

// PrecompiledContractsHomestead contains the default set of pre-compiled Ethereum
// contracts used in the Frontier and Homestead releases.
var PrecompiledContractsHomestead = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}): &ecrecover{},
	types.BytesToAddress([]byte{2}): &sha256hash{},
	types.BytesToAddress([]byte{3}): &ripemd160hash{},
	types.BytesToAddress([]byte{4}): &dataCopy{},
}

// PrecompiledContractsByzantium contains the default set of pre-compiled Ethereum
// contracts used in the Byzantium release.
var PrecompiledContractsByzantium = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}): &ecrecover{},
	types.BytesToAddress([]byte{2}): &sha256hash{},
	types.BytesToAddress([]byte{3}): &ripemd160hash{},
	types.BytesToAddress([]byte{4}): &dataCopy{},
	types.BytesToAddress([]byte{5}): &bigModExp{eip2565: false, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}): &bn256AddByzantium{},
	types.BytesToAddress([]byte{7}): &bn256ScalarMulByzantium{},
	types.BytesToAddress([]byte{8}): &bn256PairingByzantium{},
}

// PrecompiledContractsIstanbul contains the default set of pre-compiled Ethereum
// contracts used in the Istanbul release.
var PrecompiledContractsIstanbul = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}): &ecrecover{},
	types.BytesToAddress([]byte{2}): &sha256hash{},
	types.BytesToAddress([]byte{3}): &ripemd160hash{},
	types.BytesToAddress([]byte{4}): &dataCopy{},
	types.BytesToAddress([]byte{5}): &bigModExp{eip2565: false, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}): &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}): &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}): &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}): &blake2F{},
}

var PrecompiledContractsIstanbulForBSC = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}): &ecrecover{},
	types.BytesToAddress([]byte{2}): &sha256hash{},
	types.BytesToAddress([]byte{3}): &ripemd160hash{},
	types.BytesToAddress([]byte{4}): &dataCopy{},
	types.BytesToAddress([]byte{5}): &bigModExp{eip2565: false, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}): &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}): &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}): &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}): &blake2F{},

	//types.BytesToAddress([]byte{100}): &tmHeaderValidate{},
	//types.BytesToAddress([]byte{101}): &iavlMerkleProofValidate{},
}

var PrecompiledContractsNano = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}): &ecrecover{},
	types.BytesToAddress([]byte{2}): &sha256hash{},
	types.BytesToAddress([]byte{3}): &ripemd160hash{},
	types.BytesToAddress([]byte{4}): &dataCopy{},
	types.BytesToAddress([]byte{5}): &bigModExp{eip2565: false, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}): &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}): &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}): &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}): &blake2F{},

	//types.BytesToAddress([]byte{100}): &tmHeaderValidateNano{},
	//types.BytesToAddress([]byte{101}): &iavlMerkleProofValidateNano{},
}

var PrecompiledContractsIsMoran = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}): &ecrecover{},
	types.BytesToAddress([]byte{2}): &sha256hash{},
	types.BytesToAddress([]byte{3}): &ripemd160hash{},
	types.BytesToAddress([]byte{4}): &dataCopy{},
	types.BytesToAddress([]byte{5}): &bigModExp{eip2565: false, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}): &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}): &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}): &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}): &blake2F{},

	//types.BytesToAddress([]byte{100}): &tmHeaderValidate{},
	//types.BytesToAddress([]byte{101}): &iavlMerkleProofValidateMoran{},
}

// PrecompiledContractsBerlin contains the default set of pre-compiled Ethereum
// contracts used in the Berlin release.
var PrecompiledContractsBerlin = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}): &ecrecover{},
	types.BytesToAddress([]byte{2}): &sha256hash{},
	types.BytesToAddress([]byte{3}): &ripemd160hash{},
	types.BytesToAddress([]byte{4}): &dataCopy{},
	types.BytesToAddress([]byte{5}): &bigModExp{eip2565: true, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}): &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}): &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}): &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}): &blake2F{},
}

// PrecompiledContractsBLS contains the active BLS12-381 precompile layout used by
// Prague/Osaka compatibility tests. Current EEST fixtures route both single-scalar
// and multi-scalar inputs through the MSM entries, so the active address range is
// 0x0b - 0x11.
var PrecompiledContractsBLS = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{0x0b}): &bls12381G1Add{},      // BLS12_G1ADD
	types.BytesToAddress([]byte{0x0c}): &bls12381G1MultiExp{}, // BLS12_G1 scalar/MSM
	types.BytesToAddress([]byte{0x0d}): &bls12381G2Add{},      // BLS12_G2ADD
	types.BytesToAddress([]byte{0x0e}): &bls12381G2MultiExp{}, // BLS12_G2 scalar/MSM
	types.BytesToAddress([]byte{0x0f}): &bls12381Pairing{},    // BLS12_PAIRING
	types.BytesToAddress([]byte{0x10}): &bls12381MapG1{},      // BLS12_MAP_FP_TO_G1
	types.BytesToAddress([]byte{0x11}): &bls12381MapG2{},      // BLS12_MAP_FP2_TO_G2
}

// PrecompiledContractsCancun contains the default set of pre-compiled Ethereum
// contracts used in the Cancun release. Includes Berlin precompiles + EIP-4844 point evaluation.
var PrecompiledContractsCancun = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}):    &ecrecover{},
	types.BytesToAddress([]byte{2}):    &sha256hash{},
	types.BytesToAddress([]byte{3}):    &ripemd160hash{},
	types.BytesToAddress([]byte{4}):    &dataCopy{},
	types.BytesToAddress([]byte{5}):    &bigModExp{eip2565: true, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}):    &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}):    &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}):    &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}):    &blake2F{},
	types.BytesToAddress([]byte{0x0a}): &pointEvaluationPrecompile{}, // EIP-4844
}

// PrecompiledContractsPrague contains the default set of pre-compiled Ethereum
// contracts used in the Prague release. This includes Cancun precompiles plus
// the active BLS layout exercised by current execution-spec-tests.
var PrecompiledContractsPrague = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}):    &ecrecover{},
	types.BytesToAddress([]byte{2}):    &sha256hash{},
	types.BytesToAddress([]byte{3}):    &ripemd160hash{},
	types.BytesToAddress([]byte{4}):    &dataCopy{},
	types.BytesToAddress([]byte{5}):    &bigModExp{eip2565: true, eip7823: false, eip7883: false},
	types.BytesToAddress([]byte{6}):    &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}):    &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}):    &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}):    &blake2F{},
	types.BytesToAddress([]byte{0x0a}): &pointEvaluationPrecompile{}, // EIP-4844
	types.BytesToAddress([]byte{0x0b}): &bls12381G1Add{},             // EIP-2537
	types.BytesToAddress([]byte{0x0c}): &bls12381G1MultiExp{},
	types.BytesToAddress([]byte{0x0d}): &bls12381G2Add{},
	types.BytesToAddress([]byte{0x0e}): &bls12381G2MultiExp{},
	types.BytesToAddress([]byte{0x0f}): &bls12381Pairing{},
	types.BytesToAddress([]byte{0x10}): &bls12381MapG1{},
	types.BytesToAddress([]byte{0x11}): &bls12381MapG2{},
}

// PrecompiledContractsPectra contains the default set of pre-compiled Ethereum
// contracts used in the Pectra release. Same as Prague's EL surface.
// Pectra = Prague + Electra consensus changes.
var PrecompiledContractsPectra = PrecompiledContractsPrague

// PrecompiledContractsOsaka contains the default set of pre-compiled Ethereum
// contracts used in the Osaka release. Osaka keeps the Prague/Pectra active
// BLS layout, updates MODEXP for EIP-7823/EIP-7883, and adds the P-256 precompile.
var PrecompiledContractsOsaka = map[types.Address]PrecompiledContract{
	types.BytesToAddress([]byte{1}):                                  &ecrecover{},
	types.BytesToAddress([]byte{2}):                                  &sha256hash{},
	types.BytesToAddress([]byte{3}):                                  &ripemd160hash{},
	types.BytesToAddress([]byte{4}):                                  &dataCopy{},
	types.BytesToAddress([]byte{5}):                                  &bigModExp{eip2565: true, eip7823: true, eip7883: true}, // EIP-7823 + EIP-7883
	types.BytesToAddress([]byte{6}):                                  &bn256AddIstanbul{},
	types.BytesToAddress([]byte{7}):                                  &bn256ScalarMulIstanbul{},
	types.BytesToAddress([]byte{8}):                                  &bn256PairingIstanbul{},
	types.BytesToAddress([]byte{9}):                                  &blake2F{},
	types.BytesToAddress([]byte{0x0a}):                               &pointEvaluationPrecompile{}, // EIP-4844
	types.BytesToAddress([]byte{0x0b}):                               &bls12381G1Add{},             // EIP-2537
	types.BytesToAddress([]byte{0x0c}):                               &bls12381G1MultiExp{},
	types.BytesToAddress([]byte{0x0d}):                               &bls12381G2Add{},
	types.BytesToAddress([]byte{0x0e}):                               &bls12381G2MultiExp{},
	types.BytesToAddress([]byte{0x0f}):                               &bls12381Pairing{},
	types.BytesToAddress([]byte{0x10}):                               &bls12381MapG1{},
	types.BytesToAddress([]byte{0x11}):                               &bls12381MapG2{},
	types.HexToAddress("0x0000000000000000000000000000000000000100"): &p256Verify{}, // EIP-7951
}

// PrecompiledContractsFusaka contains the default set of pre-compiled Ethereum
// contracts used in the Fusaka release. Today it inherits Osaka's precompile set.
var PrecompiledContractsFusaka = PrecompiledContractsOsaka

var (
	PrecompiledAddressesMoran          []types.Address
	PrecompiledAddressesNano           []types.Address
	PrecompiledAddressesCancun         []types.Address
	PrecompiledAddressesPrague         []types.Address
	PrecompiledAddressesPectra         []types.Address
	PrecompiledAddressesOsaka          []types.Address
	PrecompiledAddressesFusaka         []types.Address
	PrecompiledAddressesBerlin         []types.Address
	PrecompiledAddressesIstanbul       []types.Address
	PrecompiledAddressesIstanbulForBSC []types.Address
	PrecompiledAddressesByzantium      []types.Address
	PrecompiledAddressesHomestead      []types.Address
)

// collectAddresses extracts all keys from a precompile contract map into a slice.
func collectAddresses(contracts map[types.Address]PrecompiledContract) []types.Address {
	addrs := make([]types.Address, 0, len(contracts))
	for k := range contracts {
		addrs = append(addrs, k)
	}
	return addrs
}

func init() {
	PrecompiledAddressesHomestead = collectAddresses(PrecompiledContractsHomestead)
	PrecompiledAddressesByzantium = collectAddresses(PrecompiledContractsByzantium)
	PrecompiledAddressesIstanbul = collectAddresses(PrecompiledContractsIstanbul)
	PrecompiledAddressesIstanbulForBSC = collectAddresses(PrecompiledContractsIstanbulForBSC)
	PrecompiledAddressesBerlin = collectAddresses(PrecompiledContractsBerlin)
	PrecompiledAddressesCancun = collectAddresses(PrecompiledContractsCancun)
	PrecompiledAddressesPrague = collectAddresses(PrecompiledContractsPrague)
	PrecompiledAddressesPectra = collectAddresses(PrecompiledContractsPectra)
	PrecompiledAddressesOsaka = collectAddresses(PrecompiledContractsOsaka)
	PrecompiledAddressesFusaka = collectAddresses(PrecompiledContractsFusaka)
	PrecompiledAddressesNano = collectAddresses(PrecompiledContractsNano)
	PrecompiledAddressesMoran = collectAddresses(PrecompiledContractsIsMoran)
}

// ActivePrecompiles returns the precompiles enabled with the current configuration.
func ActivePrecompiles(rules *params.Rules) []types.Address {
	addrs := legacyPrecompileAddressesBySet(activeLegacyPrecompileSet(rules))
	// N42 extension: append PQ precompile addresses when enabled
	if rules.IsPQPrecompiles {
		addrs = append(addrs, collectAddresses(PrecompiledContractsPQ)...)
	}
	// N42 extension: append CAS precompile address when enabled
	if rules.IsContentStore {
		addrs = append(addrs, collectAddresses(PrecompiledContractsCAS)...)
	}
	// N42 extension: append AI inference precompile address when enabled
	if rules.IsAIInference {
		addrs = append(addrs, PrecompiledAddressesAIInference...)
	}
	// N42 extension: append randomness beacon precompile address when enabled
	if rules.IsRandomness {
		addrs = append(addrs, PrecompiledAddressesRandomness...)
	}
	return addrs
}

// RunPrecompiledContract runs and evaluates the output of a precompiled contract.
// It returns
// - the returned bytes,
// - the _remaining_ gas,
// - any error that occurred
func RunPrecompiledContract(p PrecompiledContract, input []byte, suppliedGas uint64) (ret []byte, remainingGas uint64, err error) {
	gasCost := p.RequiredGas(input)
	if suppliedGas < gasCost {
		return nil, 0, ErrOutOfGas
	}
	suppliedGas -= gasCost
	output, err := p.Run(input)
	return output, suppliedGas, err
}

// ECRECOVER implemented as a native contract.
type ecrecover struct{}

func (c *ecrecover) RequiredGas(input []byte) uint64 {
	return params.EcrecoverGas
}

func (c *ecrecover) Run(input []byte) ([]byte, error) {
	const ecRecoverInputLength = 128

	input = types.RightPadBytes(input, ecRecoverInputLength)
	// "input" is (hash, v, r, s), each 32 bytes
	// but for ecrecover we want (r, s, v)

	r := new(uint256.Int).SetBytes(input[64:96])
	s := new(uint256.Int).SetBytes(input[96:128])
	v := input[63] - 27

	// tighter sig s values input homestead only apply to tx sigs
	if !allZero(input[32:63]) || !crypto.ValidateSignatureValues(v, r, s, false) {
		return nil, nil
	}
	// We must make sure not to modify the 'input', so placing the 'v' along with
	// the signature needs to be done on a new allocation
	sig := make([]byte, 65)
	copy(sig, input[64:128])
	sig[64] = v
	// v needs to be at the end for libsecp256k1
	pubKey, err := crypto.Ecrecover(input[:32], sig)
	// make sure the public key is a valid one
	if err != nil {
		return nil, nil
	}

	// the first byte of pubkey is bitcoin heritage
	return types.LeftPadBytes(crypto.Keccak256(pubKey[1:])[12:], 32), nil
}

// SHA256 implemented as a native contract.
type sha256hash struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
//
// This method does not require any overflow checking as the input size gas costs
// required for anything significant is so high it's impossible to pay for.
func (c *sha256hash) RequiredGas(input []byte) uint64 {
	return uint64(len(input)+31)/32*params.Sha256PerWordGas + params.Sha256BaseGas
}
func (c *sha256hash) Run(input []byte) ([]byte, error) {
	h := sha256.Sum256(input)
	return h[:], nil
}

// RIPEMD160 implemented as a native contract.
type ripemd160hash struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
//
// This method does not require any overflow checking as the input size gas costs
// required for anything significant is so high it's impossible to pay for.
func (c *ripemd160hash) RequiredGas(input []byte) uint64 {
	return uint64(len(input)+31)/32*params.Ripemd160PerWordGas + params.Ripemd160BaseGas
}
func (c *ripemd160hash) Run(input []byte) ([]byte, error) {
	ripemd := ripemd160.New()
	ripemd.Write(input)
	return types.LeftPadBytes(ripemd.Sum(nil), 32), nil
}

// data copy implemented as a native contract.
type dataCopy struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
//
// This method does not require any overflow checking as the input size gas costs
// required for anything significant is so high it's impossible to pay for.
func (c *dataCopy) RequiredGas(input []byte) uint64 {
	return uint64(len(input)+31)/32*params.IdentityPerWordGas + params.IdentityBaseGas
}
func (c *dataCopy) Run(in []byte) ([]byte, error) {
	return in, nil
}

// bigModExp implements a native big integer exponential modular operation.
type bigModExp struct {
	eip2565 bool
	eip7823 bool // EIP-7823: MODEXP input size limit (max 1024 bytes for base, exp, mod)
	eip7883 bool // EIP-7883: MODEXP gas cost increase (3x multiplier, min 500)
}

var (
	big1      = big.NewInt(1)
	big3      = big.NewInt(3)
	big4      = big.NewInt(4)
	big7      = big.NewInt(7)
	big8      = big.NewInt(8)
	big16     = big.NewInt(16)
	big20     = big.NewInt(20)
	big32     = big.NewInt(32)
	big64     = big.NewInt(64)
	big96     = big.NewInt(96)
	big480    = big.NewInt(480)
	big1024   = big.NewInt(1024)
	big3072   = big.NewInt(3072)
	big199680 = big.NewInt(199680)
)

// EIP-7823: Maximum input size limits for MODEXP precompile
const (
	modexpMaxInputSize = 1024 // Max bytes for base, exponent, or modulus length
)

// EIP-7883: MODEXP gas cost parameters
const (
	modexpGasMultiplierEIP7883 = 3   // Gas multiplier for EIP-7883
	modexpMinGasEIP7883        = 500 // Minimum gas cost for EIP-7883
)

var (
	// errModExpInputTooLarge is returned when MODEXP input exceeds EIP-7823 limits
	errModExpInputTooLarge = errors.New("modexp input size exceeds 1024 bytes")
)

// modexpMultComplexity implements bigModexp multComplexity formula, as defined in EIP-198
//
// def mult_complexity(x):
//
//	if x <= 64: return x ** 2
//	elif x <= 1024: return x ** 2 // 4 + 96 * x - 3072
//	else: return x ** 2 // 16 + 480 * x - 199680
//
// where is x is max(length_of_MODULUS, length_of_BASE)
func modexpMultComplexity(x *big.Int) *big.Int {
	switch {
	case x.Cmp(big64) <= 0:
		x.Mul(x, x) // x ** 2
	case x.Cmp(big1024) <= 0:
		// (x ** 2 // 4 ) + ( 96 * x - 3072)
		x = new(big.Int).Add(
			new(big.Int).Div(new(big.Int).Mul(x, x), big4),
			new(big.Int).Sub(new(big.Int).Mul(big96, x), big3072),
		)
	default:
		// (x ** 2 // 16) + (480 * x - 199680)
		x = new(big.Int).Add(
			new(big.Int).Div(new(big.Int).Mul(x, x), big16),
			new(big.Int).Sub(new(big.Int).Mul(big480, x), big199680),
		)
	}
	return x
}

// RequiredGas returns the gas required to execute the pre-compiled contract.
func (c *bigModExp) RequiredGas(input []byte) uint64 {
	var (
		baseLen = new(big.Int).SetBytes(getData(input, 0, 32))
		expLen  = new(big.Int).SetBytes(getData(input, 32, 32))
		modLen  = new(big.Int).SetBytes(getData(input, 64, 32))
	)

	// EIP-7823: Check input size limits (max 1024 bytes for each)
	if c.eip7823 {
		maxSize := big.NewInt(modexpMaxInputSize)
		if baseLen.Cmp(maxSize) > 0 || expLen.Cmp(maxSize) > 0 || modLen.Cmp(maxSize) > 0 {
			// Return max gas to indicate invalid input
			return math.MaxUint64
		}
	}

	if len(input) > 96 {
		input = input[96:]
	} else {
		input = input[:0]
	}
	// Retrieve the head 32 bytes of exp for the adjusted exponent length
	var expHead *big.Int
	if big.NewInt(int64(len(input))).Cmp(baseLen) <= 0 {
		expHead = new(big.Int)
	} else {
		if expLen.Cmp(big32) > 0 {
			expHead = new(big.Int).SetBytes(getData(input, baseLen.Uint64(), 32))
		} else {
			expHead = new(big.Int).SetBytes(getData(input, baseLen.Uint64(), expLen.Uint64()))
		}
	}
	// Calculate the adjusted exponent length
	var msb int
	if bitlen := expHead.BitLen(); bitlen > 0 {
		msb = bitlen - 1
	}
	adjExpLen := new(big.Int)
	if expLen.Cmp(big32) > 0 {
		adjExpLen.Sub(expLen, big32)
		adjExpLen.Mul(big8, adjExpLen)
	}
	adjExpLen.Add(adjExpLen, big.NewInt(int64(msb)))
	// Calculate the gas cost of the operation
	gas := new(big.Int).Set(math.BigMax(modLen, baseLen))
	if c.eip2565 {
		if c.eip7883 {
			return CalcModExpGas7883(baseLen, expLen, modLen, expHead)
		}
		// EIP-2565 has three changes
		// 1. Different multComplexity (inlined here)
		// in EIP-2565 (https://eips.ethereum.org/EIPS/eip-2565):
		//
		// def mult_complexity(x):
		//    ceiling(x/8)^2
		//
		//where is x is max(length_of_MODULUS, length_of_BASE)
		gas = gas.Add(gas, big7)
		gas = gas.Div(gas, big8)
		gas.Mul(gas, gas)

		gas.Mul(gas, math.BigMax(adjExpLen, big1))
		// 2. Different divisor (`GQUADDIVISOR`) (3)
		gas.Div(gas, big3)
		if gas.BitLen() > 64 {
			return math.MaxUint64
		}

		gasVal := gas.Uint64()

		// 3. Minimum price of 200 gas (EIP-2565)
		if gasVal < 200 {
			return 200
		}
		return gasVal
	}
	gas = modexpMultComplexity(gas)
	gas.Mul(gas, math.BigMax(adjExpLen, big1))
	gas.Div(gas, big20)

	if gas.BitLen() > 64 {
		return math.MaxUint64
	}
	return gas.Uint64()
}

func (c *bigModExp) Run(input []byte) ([]byte, error) {
	var (
		baseLenBig = new(big.Int).SetBytes(getData(input, 0, 32))
		expLenBig  = new(big.Int).SetBytes(getData(input, 32, 32))
		modLenBig  = new(big.Int).SetBytes(getData(input, 64, 32))
	)
	var (
		baseLen          = baseLenBig.Uint64()
		expLen           = expLenBig.Uint64()
		modLen           = modLenBig.Uint64()
		inputLenOverflow = baseLenBig.BitLen() > 64 || expLenBig.BitLen() > 64 || modLenBig.BitLen() > 64
	)

	// EIP-7823: Enforce input size limits (max 1024 bytes for each)
	if c.eip7823 {
		if inputLenOverflow || baseLen > modexpMaxInputSize || expLen > modexpMaxInputSize || modLen > modexpMaxInputSize {
			return nil, errModExpInputTooLarge
		}
	}

	if len(input) > 96 {
		input = input[96:]
	} else {
		input = input[:0]
	}
	// Handle a special case when both the base and mod length is zero
	if baseLen == 0 && modLen == 0 {
		return []byte{}, nil
	}
	// Retrieve the operands and execute the exponentiation
	var (
		base = new(big.Int).SetBytes(getData(input, 0, baseLen))
		exp  = new(big.Int).SetBytes(getData(input, baseLen, expLen))
		mod  = new(big.Int).SetBytes(getData(input, baseLen+expLen, modLen))
		v    []byte
	)
	switch {
	case mod.BitLen() == 0:
		// Modulo 0 is undefined, return zero
		return avmutil.LeftPadBytes([]byte{}, int(modLen)), nil
	case base.Cmp(avmutil.Big1) == 0:
		//If base == 1, then we can just return base % mod (if mod >= 1, which it is)
		v = base.Mod(base, mod).Bytes()
	//case mod.Bit(0) == 0:
	//	// Modulo is even
	//	v = math.FastExp(base, exp, mod).Bytes()
	default:
		// Modulo is odd
		v = new(big.Int).Exp(base, exp, mod).Bytes()
	}
	return avmutil.LeftPadBytes(v, int(modLen)), nil
}

// newCurvePoint unmarshals a binary blob into a bn256 elliptic curve point,
// returning it, or an error if the point is invalid.
func newCurvePoint(blob []byte) (*bn256.G1, error) {
	p := new(bn256.G1)
	if _, err := p.Unmarshal(blob); err != nil {
		return nil, err
	}
	return p, nil
}

// newTwistPoint unmarshals a binary blob into a bn256 elliptic curve point,
// returning it, or an error if the point is invalid.
func newTwistPoint(blob []byte) (*bn256.G2, error) {
	p := new(bn256.G2)
	if _, err := p.Unmarshal(blob); err != nil {
		return nil, err
	}
	return p, nil
}

// runBn256Add implements the Bn256Add precompile, referenced by both
// Byzantium and Istanbul operations.
func runBn256Add(input []byte) ([]byte, error) {
	x, err := newCurvePoint(getData(input, 0, 64))
	if err != nil {
		return nil, err
	}
	y, err := newCurvePoint(getData(input, 64, 64))
	if err != nil {
		return nil, err
	}
	res := new(bn256.G1)
	res.Add(x, y)
	return res.Marshal(), nil
}

// bn256Add implements a native elliptic curve point addition conforming to
// Istanbul consensus rules.
type bn256AddIstanbul struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
func (c *bn256AddIstanbul) RequiredGas(input []byte) uint64 {
	return params.Bn256AddGasIstanbul
}

func (c *bn256AddIstanbul) Run(input []byte) ([]byte, error) {
	return runBn256Add(input)
}

// bn256AddByzantium implements a native elliptic curve point addition
// conforming to Byzantium consensus rules.
type bn256AddByzantium struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
func (c *bn256AddByzantium) RequiredGas(input []byte) uint64 {
	return params.Bn256AddGasByzantium
}

func (c *bn256AddByzantium) Run(input []byte) ([]byte, error) {
	return runBn256Add(input)
}

// runBn256ScalarMul implements the Bn256ScalarMul precompile, referenced by
// both Byzantium and Istanbul operations.
func runBn256ScalarMul(input []byte) ([]byte, error) {
	p, err := newCurvePoint(getData(input, 0, 64))
	if err != nil {
		return nil, err
	}
	res := new(bn256.G1)
	res.ScalarMult(p, new(big.Int).SetBytes(getData(input, 64, 32)))
	return res.Marshal(), nil
}

// bn256ScalarMulIstanbul implements a native elliptic curve scalar
// multiplication conforming to Istanbul consensus rules.
type bn256ScalarMulIstanbul struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
func (c *bn256ScalarMulIstanbul) RequiredGas(input []byte) uint64 {
	return params.Bn256ScalarMulGasIstanbul
}

func (c *bn256ScalarMulIstanbul) Run(input []byte) ([]byte, error) {
	return runBn256ScalarMul(input)
}

// bn256ScalarMulByzantium implements a native elliptic curve scalar
// multiplication conforming to Byzantium consensus rules.
type bn256ScalarMulByzantium struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
func (c *bn256ScalarMulByzantium) RequiredGas(input []byte) uint64 {
	return params.Bn256ScalarMulGasByzantium
}

func (c *bn256ScalarMulByzantium) Run(input []byte) ([]byte, error) {
	return runBn256ScalarMul(input)
}

var (
	// true32Byte is returned if the bn256 pairing check succeeds.
	true32Byte = types.LeftPadBytes([]byte{1}, 32)

	// false32Byte is returned if the bn256 pairing check fails.
	false32Byte = make([]byte, 32)

	// errBadPairingInput is returned if the bn256 pairing input is invalid.
	errBadPairingInput = errors.New("bad elliptic curve pairing size")
)

// runBn256Pairing implements the Bn256Pairing precompile, referenced by both
// Byzantium and Istanbul operations.
func runBn256Pairing(input []byte) ([]byte, error) {
	// Handle some corner cases cheaply
	if len(input)%192 > 0 {
		return nil, errBadPairingInput
	}
	// Convert the input into a set of coordinates
	var (
		cs []*bn256.G1
		ts []*bn256.G2
	)
	for i := 0; i < len(input); i += 192 {
		c, err := newCurvePoint(input[i : i+64])
		if err != nil {
			return nil, err
		}
		t, err := newTwistPoint(input[i+64 : i+192])
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
		ts = append(ts, t)
	}
	// Execute the pairing checks and return the results
	if bn256.PairingCheck(cs, ts) {
		return true32Byte, nil
	}
	return false32Byte, nil
}

// bn256PairingIstanbul implements a pairing pre-compile for the bn256 curve
// conforming to Istanbul consensus rules.
type bn256PairingIstanbul struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
func (c *bn256PairingIstanbul) RequiredGas(input []byte) uint64 {
	return params.Bn256PairingBaseGasIstanbul + uint64(len(input)/192)*params.Bn256PairingPerPointGasIstanbul
}

func (c *bn256PairingIstanbul) Run(input []byte) ([]byte, error) {
	return runBn256Pairing(input)
}

// bn256PairingByzantium implements a pairing pre-compile for the bn256 curve
// conforming to Byzantium consensus rules.
type bn256PairingByzantium struct{}

// RequiredGas returns the gas required to execute the pre-compiled contract.
func (c *bn256PairingByzantium) RequiredGas(input []byte) uint64 {
	return params.Bn256PairingBaseGasByzantium + uint64(len(input)/192)*params.Bn256PairingPerPointGasByzantium
}

func (c *bn256PairingByzantium) Run(input []byte) ([]byte, error) {
	return runBn256Pairing(input)
}

type blake2F struct{}

func (c *blake2F) RequiredGas(input []byte) uint64 {
	// If the input is malformed, we can't calculate the gas, return 0 and let the
	// actual call choke and fault.
	if len(input) != blake2FInputLength {
		return 0
	}
	return uint64(binary.BigEndian.Uint32(input[0:4]))
}

const (
	blake2FInputLength        = 213
	blake2FFinalBlockBytes    = byte(1)
	blake2FNonFinalBlockBytes = byte(0)
)

var (
	errBlake2FInvalidInputLength = errors.New("invalid input length")
	errBlake2FInvalidFinalFlag   = errors.New("invalid final flag")
)

func (c *blake2F) Run(input []byte) ([]byte, error) {
	// Make sure the input is valid (correct length and final flag)
	if len(input) != blake2FInputLength {
		return nil, errBlake2FInvalidInputLength
	}
	if input[212] != blake2FNonFinalBlockBytes && input[212] != blake2FFinalBlockBytes {
		return nil, errBlake2FInvalidFinalFlag
	}
	// Parse the input into the Blake2b call parameters
	var (
		rounds = binary.BigEndian.Uint32(input[0:4])
		final  = input[212] == blake2FFinalBlockBytes

		h [8]uint64
		m [16]uint64
		t [2]uint64
	)
	for i := 0; i < 8; i++ {
		offset := 4 + i*8
		h[i] = binary.LittleEndian.Uint64(input[offset : offset+8])
	}
	for i := 0; i < 16; i++ {
		offset := 68 + i*8
		m[i] = binary.LittleEndian.Uint64(input[offset : offset+8])
	}
	t[0] = binary.LittleEndian.Uint64(input[196:204])
	t[1] = binary.LittleEndian.Uint64(input[204:212])

	// Execute the compression function, extract and return the result
	blake2b.F(&h, m, t, final, rounds)

	output := make([]byte, 64)
	for i := 0; i < 8; i++ {
		offset := i * 8
		binary.LittleEndian.PutUint64(output[offset:offset+8], h[i])
	}
	return output, nil
}

// Precompile getter functions provide access to precompile instances for the Registry.

// GetEcrecover returns an ecrecover precompile instance.
func GetEcrecover() PrecompiledContract { return &ecrecover{} }

// GetSha256 returns a SHA256 precompile instance.
func GetSha256() PrecompiledContract { return &sha256hash{} }

// GetRipemd160 returns a RIPEMD160 precompile instance.
func GetRipemd160() PrecompiledContract { return &ripemd160hash{} }

// GetDataCopy returns a data copy precompile instance.
func GetDataCopy() PrecompiledContract { return &dataCopy{} }

// GetBigModExp returns a big modular exponentiation precompile instance.
// Parameters:
//   - eip2565: enables EIP-2565 gas repricing (Berlin+)
//   - eip7823: enables EIP-7823 input size limits (max 1024 bytes, Osaka+)
//   - eip7883: enables EIP-7883 gas cost increase (3x multiplier, min 500, Osaka+)
func GetBigModExp(eip2565, eip7823, eip7883 bool) PrecompiledContract {
	return &bigModExp{eip2565: eip2565, eip7823: eip7823, eip7883: eip7883}
}

// GetBn256Add returns a BN256 addition precompile instance.
func GetBn256Add(istanbul bool) PrecompiledContract {
	if istanbul {
		return &bn256AddIstanbul{}
	}
	return &bn256AddByzantium{}
}

// GetBn256ScalarMul returns a BN256 scalar multiplication precompile instance.
func GetBn256ScalarMul(istanbul bool) PrecompiledContract {
	if istanbul {
		return &bn256ScalarMulIstanbul{}
	}
	return &bn256ScalarMulByzantium{}
}

// GetBn256Pairing returns a BN256 pairing precompile instance.
func GetBn256Pairing(istanbul bool) PrecompiledContract {
	if istanbul {
		return &bn256PairingIstanbul{}
	}
	return &bn256PairingByzantium{}
}

// GetBlake2F returns a BLAKE2b F compression function precompile instance.
func GetBlake2F() PrecompiledContract { return &blake2F{} }

// GetBls12381G1Add returns a BLS12-381 G1 addition precompile instance.
func GetBls12381G1Add() PrecompiledContract { return &bls12381G1Add{} }

// GetBls12381G1Mul returns a BLS12-381 G1 multiplication precompile instance.
func GetBls12381G1Mul() PrecompiledContract { return &bls12381G1Mul{} }

// GetBls12381G1MultiExp returns a BLS12-381 G1 multi-exponentiation precompile instance.
func GetBls12381G1MultiExp() PrecompiledContract { return &bls12381G1MultiExp{} }

// GetBls12381G2Add returns a BLS12-381 G2 addition precompile instance.
func GetBls12381G2Add() PrecompiledContract { return &bls12381G2Add{} }

// GetBls12381G2Mul returns a BLS12-381 G2 multiplication precompile instance.
func GetBls12381G2Mul() PrecompiledContract { return &bls12381G2Mul{} }

// GetBls12381G2MultiExp returns a BLS12-381 G2 multi-exponentiation precompile instance.
func GetBls12381G2MultiExp() PrecompiledContract { return &bls12381G2MultiExp{} }

// GetBls12381Pairing returns a BLS12-381 pairing precompile instance.
func GetBls12381Pairing() PrecompiledContract { return &bls12381Pairing{} }

// GetBls12381MapG1 returns a BLS12-381 map to G1 precompile instance.
func GetBls12381MapG1() PrecompiledContract { return &bls12381MapG1{} }

// GetBls12381MapG2 returns a BLS12-381 map to G2 precompile instance.
func GetBls12381MapG2() PrecompiledContract { return &bls12381MapG2{} }
