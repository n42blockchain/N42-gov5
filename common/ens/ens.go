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

// Package ens implements Ethereum Name Service (ENS) support.
//
// ENS is a distributed, open, and extensible naming system based on the Ethereum blockchain.
// It maps human-readable names like 'alice.eth' to machine-readable identifiers such as
// Ethereum addresses, content hashes, and metadata.
//
// Reference: https://docs.ens.domains/

package ens

import (
	"errors"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"golang.org/x/crypto/sha3"
)

// =============================================================================
// ENS Contract Addresses
// =============================================================================

// Registry addresses for different networks
var (
	// MainnetRegistryAddress is the ENS registry address on Ethereum mainnet
	MainnetRegistryAddress = types.HexToAddress("0x00000000000C2E074eC69A0dFb2997BA6C7d2e1e")

	// SepoliaRegistryAddress is the ENS registry address on Sepolia testnet
	SepoliaRegistryAddress = types.HexToAddress("0x00000000000C2E074eC69A0dFb2997BA6C7d2e1e")

	// GoerliRegistryAddress is the ENS registry address on Goerli testnet (deprecated)
	GoerliRegistryAddress = types.HexToAddress("0x00000000000C2E074eC69A0dFb2997BA6C7d2e1e")

	// HoleskyRegistryAddress is the ENS registry address on Holesky testnet
	HoleskyRegistryAddress = types.HexToAddress("0x00000000000C2E074eC69A0dFb2997BA6C7d2e1e")
)

// Resolver addresses
var (
	// PublicResolverAddress is the public resolver contract address on mainnet
	PublicResolverAddress = types.HexToAddress("0x231b0Ee14048e9dCcD1d247744d114a4EB5E8E63")

	// UniversalResolverAddress is the universal resolver for wildcard resolution
	UniversalResolverAddress = types.HexToAddress("0xc0497E381f536Be9ce14B0dD3817cBcAe57d2F62")
)

// Reverse registrar
var (
	// ReverseRegistrarAddress is the reverse registrar contract
	ReverseRegistrarAddress = types.HexToAddress("0xa58E81fe9b61B5c3fE2AFD33CF304c454AbFc7Cb")
)

// =============================================================================
// ENS Constants
// =============================================================================

// Namehash of common domains
var (
	// ETHNamehash is the namehash of "eth"
	ETHNamehash = types.HexToHash("0x93cdeb708b7545dc668eb9280176169d1c33cfd8ed6f04690a0bcc88a93fc4ae")

	// ReverseNamehash is the namehash of "addr.reverse"
	ReverseNamehash = types.HexToHash("0x91d1777781884d03a6757a803996e38de2a42967fb37eeaca72729271025a9e2")
)

// Interface IDs for EIP-165
var (
	// InterfaceAddrResolver is the interface ID for addr(bytes32) resolver
	InterfaceAddrResolver = [4]byte{0x3b, 0x3b, 0x57, 0xde}

	// InterfaceAddressResolver is the interface ID for addr(bytes32,uint256) resolver
	InterfaceAddressResolver = [4]byte{0xf1, 0xcb, 0x7e, 0x06}

	// InterfaceTextResolver is the interface ID for text(bytes32,string) resolver
	InterfaceTextResolver = [4]byte{0x59, 0xd1, 0xd4, 0x3c}

	// InterfaceContentHashResolver is the interface ID for contenthash(bytes32) resolver
	InterfaceContentHashResolver = [4]byte{0xbc, 0x1c, 0x58, 0xd1}

	// InterfaceNameResolver is the interface ID for name(bytes32) resolver
	InterfaceNameResolver = [4]byte{0x69, 0x1f, 0x34, 0x31}

	// InterfaceABIResolver is the interface ID for ABI(bytes32,uint256) resolver
	InterfaceABIResolver = [4]byte{0x21, 0x03, 0xab, 0x68}

	// InterfacePubkeyResolver is the interface ID for pubkey(bytes32) resolver
	InterfacePubkeyResolver = [4]byte{0xc8, 0x69, 0x02, 0x33}
)

// =============================================================================
// ENS Errors
// =============================================================================

var (
	// ErrInvalidName is returned for invalid ENS names
	ErrInvalidName = errors.New("invalid ENS name")

	// ErrNoResolver is returned when no resolver is set for a name
	ErrNoResolver = errors.New("no resolver set for name")

	// ErrResolverNotFound is returned when resolver contract doesn't exist
	ErrResolverNotFound = errors.New("resolver not found")

	// ErrResolutionFailed is returned when resolution fails
	ErrResolutionFailed = errors.New("resolution failed")

	// ErrNotOwner is returned when caller is not the owner
	ErrNotOwner = errors.New("not owner of name")

	// ErrNameExpired is returned when the name has expired
	ErrNameExpired = errors.New("name has expired")

	// ErrNameNotAvailable is returned when name is not available for registration
	ErrNameNotAvailable = errors.New("name not available")

	// ErrInvalidContentHash is returned for invalid content hash
	ErrInvalidContentHash = errors.New("invalid content hash")
)

// =============================================================================
// Namehash Implementation
// =============================================================================

// Namehash calculates the namehash of an ENS name.
// The namehash is a recursive hash function defined as:
//   - namehash('') = 0x0000000000000000000000000000000000000000000000000000000000000000
//   - namehash(name) = keccak256(namehash(parent) + keccak256(label))
//
// Example: namehash("foo.eth") = keccak256(namehash("eth") + keccak256("foo"))
func Namehash(name string) types.Hash {
	if name == "" {
		return types.Hash{}
	}

	// Normalize the name (lowercase)
	name = Normalize(name)

	// Split into labels
	labels := strings.Split(name, ".")

	// Calculate namehash from right to left
	node := types.Hash{}
	for i := len(labels) - 1; i >= 0; i-- {
		label := labels[i]
		if label == "" {
			continue
		}

		// Hash the label
		labelHash := keccak256([]byte(label))

		// Hash node + labelHash
		node = keccak256(append(node[:], labelHash[:]...))
	}

	return node
}

// LabelHash calculates the keccak256 hash of a label
func LabelHash(label string) types.Hash {
	return keccak256([]byte(Normalize(label)))
}

// Normalize normalizes an ENS name to lowercase
func Normalize(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// keccak256 calculates the keccak256 hash
func keccak256(data []byte) types.Hash {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var hash types.Hash
	h.Sum(hash[:0])
	return hash
}

// =============================================================================
// ENS Name Validation
// =============================================================================

// IsValidName checks if an ENS name is valid
func IsValidName(name string) bool {
	if name == "" {
		return false
	}

	name = Normalize(name)
	labels := strings.Split(name, ".")

	for _, label := range labels {
		if !isValidLabel(label) {
			return false
		}
	}

	return true
}

// isValidLabel checks if a single label is valid
func isValidLabel(label string) bool {
	if label == "" {
		return false
	}

	// Label must not start or end with hyphen
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}

	// Check each character
	for _, c := range label {
		if !isValidLabelChar(c) {
			return false
		}
	}

	return true
}

// isValidLabelChar checks if a character is valid in a label
func isValidLabelChar(c rune) bool {
	// Alphanumeric and hyphen only
	return (c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-'
}

// =============================================================================
// Reverse Resolution
// =============================================================================

// ReverseNode calculates the namehash for reverse resolution of an address
// The reverse node is: namehash(address.addr.reverse)
func ReverseNode(addr types.Address) types.Hash {
	// Convert address to lowercase hex without 0x prefix
	hexAddr := strings.ToLower(addr.Hex()[2:])

	// Build reverse name: {address}.addr.reverse
	reverseName := hexAddr + ".addr.reverse"

	return Namehash(reverseName)
}

// =============================================================================
// DNS Encoding
// =============================================================================

// DNSEncodeName encodes an ENS name in DNS wire format
func DNSEncodeName(name string) []byte {
	if name == "" {
		return []byte{0}
	}

	name = Normalize(name)
	labels := strings.Split(name, ".")

	var encoded []byte
	for _, label := range labels {
		if label == "" {
			continue
		}
		// Length byte + label
		encoded = append(encoded, byte(len(label)))
		encoded = append(encoded, []byte(label)...)
	}
	// Terminating zero
	encoded = append(encoded, 0)

	return encoded
}

// DNSDecodeName decodes a DNS wire format name
func DNSDecodeName(data []byte) (string, error) {
	if len(data) == 0 {
		return "", ErrInvalidName
	}

	var labels []string
	pos := 0

	for pos < len(data) {
		length := int(data[pos])
		if length == 0 {
			break
		}
		pos++

		if pos+length > len(data) {
			return "", ErrInvalidName
		}

		labels = append(labels, string(data[pos:pos+length]))
		pos += length
	}

	return strings.Join(labels, "."), nil
}

// =============================================================================
// Content Hash Encoding/Decoding
// =============================================================================

// ContentHash types
const (
	ContentHashIPFS    = 0xe3 // IPFS (CIDv0 or CIDv1)
	ContentHashSwarm   = 0xe4 // Swarm
	ContentHashOnion   = 0xbc // Onion (Tor)
	ContentHashOnion3  = 0xbd // Onion3 (Tor v3)
	ContentHashSkynet  = 0x90 // Skynet
	ContentHashArweave = 0xb2 // Arweave
)

// EncodeContentHash encodes a content hash with type prefix
func EncodeContentHash(hashType byte, hash []byte) []byte {
	return append([]byte{hashType}, hash...)
}

// DecodeContentHash decodes a content hash, returning type and hash
func DecodeContentHash(data []byte) (byte, []byte, error) {
	if len(data) < 2 {
		return 0, nil, ErrInvalidContentHash
	}
	return data[0], data[1:], nil
}

// =============================================================================
// Registry Contract ABI
// =============================================================================

// Method selectors for ENS Registry contract
var (
	// owner(bytes32) - Get owner of a node
	OwnerSelector = [4]byte{0x02, 0x57, 0x17, 0x92}

	// resolver(bytes32) - Get resolver of a node
	ResolverSelector = [4]byte{0x01, 0x78, 0xb8, 0xbf}

	// ttl(bytes32) - Get TTL of a node
	TTLSelector = [4]byte{0x16, 0xa2, 0x5c, 0xbd}

	// recordExists(bytes32) - Check if record exists
	RecordExistsSelector = [4]byte{0xf7, 0x9f, 0xe5, 0x38}

	// setOwner(bytes32,address) - Set owner of a node
	SetOwnerSelector = [4]byte{0x5b, 0x0f, 0xc9, 0xc3}

	// setResolver(bytes32,address) - Set resolver of a node
	SetResolverSelector = [4]byte{0x1e, 0x83, 0x40, 0x9a}

	// setTTL(bytes32,uint64) - Set TTL of a node
	SetTTLSelector = [4]byte{0x14, 0xab, 0x90, 0x38}

	// setSubnodeOwner(bytes32,bytes32,address) - Set subnode owner
	SetSubnodeOwnerSelector = [4]byte{0x06, 0xab, 0x59, 0x23}

	// setSubnodeRecord(bytes32,bytes32,address,address,uint64) - Set subnode record
	SetSubnodeRecordSelector = [4]byte{0x5e, 0xf2, 0xc7, 0xf0}

	// setRecord(bytes32,address,address,uint64) - Set record
	SetRecordSelector = [4]byte{0xcf, 0x40, 0x88, 0x23}

	// setApprovalForAll(address,bool) - Set approval for all
	SetApprovalForAllSelector = [4]byte{0xa2, 0x2c, 0xb4, 0x65}

	// isApprovedForAll(address,address) - Check approval
	IsApprovedForAllSelector = [4]byte{0xe9, 0x85, 0xe9, 0xc5}
)

// =============================================================================
// Resolver Contract ABI
// =============================================================================

// Method selectors for ENS Resolver contract
var (
	// addr(bytes32) - Get address for node
	AddrSelector = [4]byte{0x3b, 0x3b, 0x57, 0xde}

	// addr(bytes32,uint256) - Get address for node and coin type
	AddrCoinTypeSelector = [4]byte{0xf1, 0xcb, 0x7e, 0x06}

	// setAddr(bytes32,address) - Set address for node
	SetAddrSelector = [4]byte{0xd5, 0xfa, 0x2b, 0x00}

	// setAddr(bytes32,uint256,bytes) - Set address for node and coin type
	SetAddrCoinTypeSelector = [4]byte{0x8b, 0x95, 0xdd, 0x71}

	// text(bytes32,string) - Get text record
	TextSelector = [4]byte{0x59, 0xd1, 0xd4, 0x3c}

	// setText(bytes32,string,string) - Set text record
	SetTextSelector = [4]byte{0x10, 0xf1, 0x3a, 0x8c}

	// contenthash(bytes32) - Get content hash
	ContenthashSelector = [4]byte{0xbc, 0x1c, 0x58, 0xd1}

	// setContenthash(bytes32,bytes) - Set content hash
	SetContenthashSelector = [4]byte{0x30, 0x4e, 0x6a, 0xde}

	// name(bytes32) - Get name (for reverse resolution)
	NameSelector = [4]byte{0x69, 0x1f, 0x34, 0x31}

	// setName(bytes32,string) - Set name
	SetNameSelector = [4]byte{0x77, 0x37, 0x22, 0x13}

	// pubkey(bytes32) - Get public key
	PubkeySelector = [4]byte{0xc8, 0x69, 0x02, 0x33}

	// setPubkey(bytes32,bytes32,bytes32) - Set public key
	SetPubkeySelector = [4]byte{0x29, 0xcd, 0x62, 0xea}

	// ABI(bytes32,uint256) - Get ABI
	ABISelector = [4]byte{0x21, 0x03, 0xab, 0x68}

	// setABI(bytes32,uint256,bytes) - Set ABI
	SetABISelector = [4]byte{0x62, 0x3f, 0xd8, 0xf1}

	// supportsInterface(bytes4) - EIP-165
	SupportsInterfaceSelector = [4]byte{0x01, 0xff, 0xc9, 0xa7}
)

// =============================================================================
// Common Text Record Keys
// =============================================================================

// Standard text record keys
const (
	TextRecordEmail       = "email"
	TextRecordURL         = "url"
	TextRecordAvatar      = "avatar"
	TextRecordDescription = "description"
	TextRecordNotice      = "notice"
	TextRecordKeywords    = "keywords"
	TextRecordDiscord     = "com.discord"
	TextRecordGithub      = "com.github"
	TextRecordReddit      = "com.reddit"
	TextRecordTwitter     = "com.twitter"
	TextRecordTelegram    = "org.telegram"
)

// =============================================================================
// Coin Types (SLIP-44)
// =============================================================================

// Common coin types for multi-chain address resolution
const (
	CoinTypeETH  = 60   // Ethereum
	CoinTypeBTC  = 0    // Bitcoin
	CoinTypeLTC  = 2    // Litecoin
	CoinTypeDOGE = 3    // Dogecoin
	CoinTypeBNB  = 714  // BNB Chain
	CoinTypeMATIC = 966 // Polygon
	CoinTypeSOL  = 501  // Solana
	CoinTypeAVAX = 9000 // Avalanche
)

// =============================================================================
// Utility Functions
// =============================================================================

// GetRegistryAddress returns the ENS registry address for a given chain ID
func GetRegistryAddress(chainID uint64) types.Address {
	switch chainID {
	case 1: // Mainnet
		return MainnetRegistryAddress
	case 11155111: // Sepolia
		return SepoliaRegistryAddress
	case 5: // Goerli (deprecated)
		return GoerliRegistryAddress
	case 17000: // Holesky
		return HoleskyRegistryAddress
	default:
		return MainnetRegistryAddress // Default to mainnet
	}
}

// IsETHName checks if a name ends with .eth
func IsETHName(name string) bool {
	return strings.HasSuffix(Normalize(name), ".eth")
}

// GetParentName returns the parent name of an ENS name
func GetParentName(name string) string {
	name = Normalize(name)
	parts := strings.SplitN(name, ".", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// GetLabel returns the first label of an ENS name
func GetLabel(name string) string {
	name = Normalize(name)
	parts := strings.SplitN(name, ".", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

