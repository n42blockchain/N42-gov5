package avmtypes

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

// var EmptyUncleHash = rlpHash([]*Header(nil))
var EmptyUncleHash = types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")

type BlockNonce [8]byte

// EncodeNonce converts the given integer to a block nonce.
func EncodeNonce(i uint64) BlockNonce {
	var n BlockNonce
	binary.BigEndian.PutUint64(n[:], i)
	return n
}

// Uint64 returns the integer value of a block nonce.
func (n BlockNonce) Uint64() uint64 {
	return binary.BigEndian.Uint64(n[:])
}

// MarshalText encodes n as a hex string with 0x prefix.
func (n BlockNonce) MarshalText() ([]byte, error) {
	return hexutil.Bytes(n[:]).MarshalText()
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (n *BlockNonce) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("BlockNonce", input, n[:])
}

type Header struct {
	ParentHash  avmutil.Hash    `json:"parentHash"       gencodec:"required"`
	UncleHash   avmutil.Hash    `json:"sha3Uncles"       gencodec:"required"`
	Coinbase    avmutil.Address `json:"miner"`
	Root        avmutil.Hash    `json:"stateRoot"        gencodec:"required"`
	TxHash      avmutil.Hash    `json:"transactionsRoot" gencodec:"required"`
	ReceiptHash avmutil.Hash    `json:"receiptsRoot"     gencodec:"required"`
	Bloom       Bloom           `json:"logsBloom"        gencodec:"required"`
	Difficulty  *big.Int        `json:"difficulty"       gencodec:"required"`
	Number      *big.Int        `json:"number"           gencodec:"required"`
	GasLimit    uint64          `json:"gasLimit"         gencodec:"required"`
	GasUsed     uint64          `json:"gasUsed"          gencodec:"required"`
	Time        uint64          `json:"timestamp"        gencodec:"required"`
	Extra       []byte          `json:"extraData"        gencodec:"required"`
	MixDigest   avmutil.Hash    `json:"mixHash"`
	Nonce       BlockNonce      `json:"nonce"`

	// BaseFee was added by EIP-1559 and is ignored in legacy headers.
	BaseFee *big.Int `json:"baseFeePerGas" rlp:"optional"`

	// Post-London Ethereum header extensions.
	WithdrawalsHash  *avmutil.Hash `json:"withdrawalsRoot,omitempty" rlp:"optional"`
	BlobGasUsed      *uint64       `json:"blobGasUsed,omitempty" rlp:"optional"`
	ExcessBlobGas    *uint64       `json:"excessBlobGas,omitempty" rlp:"optional"`
	ParentBeaconRoot *avmutil.Hash `json:"parentBeaconBlockRoot,omitempty" rlp:"optional"`
	RequestsHash     *avmutil.Hash `json:"requestsHash,omitempty" rlp:"optional"`

	/*
		TODO (MariusVanDerWijden) Add this field once needed
		// Random was added during the merge and contains the BeaconState randomness
		Random avmutil.Hash `json:"random" rlp:"optional"`
	*/
}

// Hash returns the block hash of the header, which is simply the keccak256 hash of its
// RLP encoding.
func (h *Header) Hash() avmutil.Hash {
	return rlpHash(h)
}

var headerSize = avmutil.StorageSize(reflect.TypeOf(Header{}).Size())

// Size returns the approximate memory used by all internal contents. It is used
// to approximate and limit the memory consumption of various caches.
func (h *Header) Size() avmutil.StorageSize {
	var bigIntBits int
	if h.BaseFee != nil {
		bigIntBits += h.BaseFee.BitLen()
	}
	if h.Difficulty != nil {
		bigIntBits += h.Difficulty.BitLen()
	}
	if h.Number != nil {
		bigIntBits += h.Number.BitLen()
	}
	return headerSize + avmutil.StorageSize(len(h.Extra)+bigIntBits/8)
}

// MarshalJSON marshals as JSON.
func (h Header) MarshalJSON() ([]byte, error) {
	type Header struct {
		ParentHash       avmutil.Hash    `json:"parentHash"       gencodec:"required"`
		UncleHash        avmutil.Hash    `json:"sha3Uncles"       gencodec:"required"`
		Coinbase         avmutil.Address `json:"miner"`
		Root             avmutil.Hash    `json:"stateRoot"        gencodec:"required"`
		TxHash           avmutil.Hash    `json:"transactionsRoot" gencodec:"required"`
		ReceiptHash      avmutil.Hash    `json:"receiptsRoot"     gencodec:"required"`
		Bloom            Bloom           `json:"logsBloom"        gencodec:"required"`
		Difficulty       *hexutil.Big    `json:"difficulty"       gencodec:"required"`
		Number           *hexutil.Big    `json:"number"           gencodec:"required"`
		GasLimit         hexutil.Uint64  `json:"gasLimit"         gencodec:"required"`
		GasUsed          hexutil.Uint64  `json:"gasUsed"          gencodec:"required"`
		Time             hexutil.Uint64  `json:"timestamp"        gencodec:"required"`
		Extra            hexutil.Bytes   `json:"extraData"        gencodec:"required"`
		MixDigest        avmutil.Hash    `json:"mixHash"`
		Nonce            BlockNonce      `json:"nonce"`
		BaseFee          *hexutil.Big    `json:"baseFeePerGas" rlp:"optional"`
		WithdrawalsHash  *avmutil.Hash   `json:"withdrawalsRoot,omitempty" rlp:"optional"`
		BlobGasUsed      *hexutil.Uint64 `json:"blobGasUsed,omitempty" rlp:"optional"`
		ExcessBlobGas    *hexutil.Uint64 `json:"excessBlobGas,omitempty" rlp:"optional"`
		ParentBeaconRoot *avmutil.Hash   `json:"parentBeaconBlockRoot,omitempty" rlp:"optional"`
		RequestsHash     *avmutil.Hash   `json:"requestsHash,omitempty" rlp:"optional"`
		Hash             avmutil.Hash    `json:"hash"`
	}
	var enc Header
	enc.ParentHash = h.ParentHash
	enc.UncleHash = h.UncleHash
	enc.Coinbase = h.Coinbase
	enc.Root = h.Root
	enc.TxHash = h.TxHash
	enc.ReceiptHash = h.ReceiptHash
	enc.Bloom = h.Bloom
	enc.Difficulty = (*hexutil.Big)(h.Difficulty)
	enc.Number = (*hexutil.Big)(h.Number)
	enc.GasLimit = hexutil.Uint64(h.GasLimit)
	enc.GasUsed = hexutil.Uint64(h.GasUsed)
	enc.Time = hexutil.Uint64(h.Time)
	enc.Extra = h.Extra
	enc.MixDigest = h.MixDigest
	enc.Nonce = h.Nonce
	enc.BaseFee = (*hexutil.Big)(h.BaseFee)
	enc.WithdrawalsHash = h.WithdrawalsHash
	enc.BlobGasUsed = uint64PtrToHexutil(h.BlobGasUsed)
	enc.ExcessBlobGas = uint64PtrToHexutil(h.ExcessBlobGas)
	enc.ParentBeaconRoot = h.ParentBeaconRoot
	enc.RequestsHash = h.RequestsHash
	enc.Hash = h.Hash()
	return json.Marshal(&enc)
}

// UnmarshalJSON unmarshals from JSON.
func (h *Header) UnmarshalJSON(input []byte) error {
	type Header struct {
		ParentHash       *avmutil.Hash    `json:"parentHash"       gencodec:"required"`
		UncleHash        *avmutil.Hash    `json:"sha3Uncles"       gencodec:"required"`
		Coinbase         *avmutil.Address `json:"miner"`
		Root             *avmutil.Hash    `json:"stateRoot"        gencodec:"required"`
		TxHash           *avmutil.Hash    `json:"transactionsRoot" gencodec:"required"`
		ReceiptHash      *avmutil.Hash    `json:"receiptsRoot"     gencodec:"required"`
		Bloom            *Bloom           `json:"logsBloom"        gencodec:"required"`
		Difficulty       *hexutil.Big     `json:"difficulty"       gencodec:"required"`
		Number           *hexutil.Big     `json:"number"           gencodec:"required"`
		GasLimit         *hexutil.Uint64  `json:"gasLimit"         gencodec:"required"`
		GasUsed          *hexutil.Uint64  `json:"gasUsed"          gencodec:"required"`
		Time             *hexutil.Uint64  `json:"timestamp"        gencodec:"required"`
		Extra            *hexutil.Bytes   `json:"extraData"        gencodec:"required"`
		MixDigest        *avmutil.Hash    `json:"mixHash"`
		Nonce            *BlockNonce      `json:"nonce"`
		BaseFee          *hexutil.Big     `json:"baseFeePerGas" rlp:"optional"`
		WithdrawalsHash  *avmutil.Hash    `json:"withdrawalsRoot,omitempty" rlp:"optional"`
		BlobGasUsed      *hexutil.Uint64  `json:"blobGasUsed,omitempty" rlp:"optional"`
		ExcessBlobGas    *hexutil.Uint64  `json:"excessBlobGas,omitempty" rlp:"optional"`
		ParentBeaconRoot *avmutil.Hash    `json:"parentBeaconBlockRoot,omitempty" rlp:"optional"`
		RequestsHash     *avmutil.Hash    `json:"requestsHash,omitempty" rlp:"optional"`
	}
	var dec Header
	if err := json.Unmarshal(input, &dec); err != nil {
		return err
	}
	if dec.ParentHash == nil {
		return errors.New("missing required field 'parentHash' for Header")
	}
	h.ParentHash = *dec.ParentHash
	if dec.UncleHash == nil {
		return errors.New("missing required field 'sha3Uncles' for Header")
	}
	h.UncleHash = *dec.UncleHash
	if dec.Coinbase != nil {
		h.Coinbase = *dec.Coinbase
	}
	if dec.Root == nil {
		return errors.New("missing required field 'stateRoot' for Header")
	}
	h.Root = *dec.Root
	if dec.TxHash == nil {
		return errors.New("missing required field 'transactionsRoot' for Header")
	}
	h.TxHash = *dec.TxHash
	if dec.ReceiptHash == nil {
		return errors.New("missing required field 'receiptsRoot' for Header")
	}
	h.ReceiptHash = *dec.ReceiptHash
	if dec.Bloom == nil {
		return errors.New("missing required field 'logsBloom' for Header")
	}
	h.Bloom = *dec.Bloom
	if dec.Difficulty == nil {
		return errors.New("missing required field 'difficulty' for Header")
	}
	h.Difficulty = (*big.Int)(dec.Difficulty)
	if dec.Number == nil {
		return errors.New("missing required field 'number' for Header")
	}
	h.Number = (*big.Int)(dec.Number)
	if dec.GasLimit == nil {
		return errors.New("missing required field 'gasLimit' for Header")
	}
	h.GasLimit = uint64(*dec.GasLimit)
	if dec.GasUsed == nil {
		return errors.New("missing required field 'gasUsed' for Header")
	}
	h.GasUsed = uint64(*dec.GasUsed)
	if dec.Time == nil {
		return errors.New("missing required field 'timestamp' for Header")
	}
	h.Time = uint64(*dec.Time)
	if dec.Extra == nil {
		return errors.New("missing required field 'extraData' for Header")
	}
	h.Extra = *dec.Extra
	if dec.MixDigest != nil {
		h.MixDigest = *dec.MixDigest
	}
	if dec.Nonce != nil {
		h.Nonce = *dec.Nonce
	}
	if dec.BaseFee != nil {
		h.BaseFee = (*big.Int)(dec.BaseFee)
	}
	h.WithdrawalsHash = dec.WithdrawalsHash
	h.BlobGasUsed = hexutilUint64PtrToUint64(dec.BlobGasUsed)
	h.ExcessBlobGas = hexutilUint64PtrToUint64(dec.ExcessBlobGas)
	h.ParentBeaconRoot = dec.ParentBeaconRoot
	h.RequestsHash = dec.RequestsHash
	return nil
}

func uint64PtrToHexutil(v *uint64) *hexutil.Uint64 {
	if v == nil {
		return nil
	}
	u := hexutil.Uint64(*v)
	return &u
}

func hexutilUint64PtrToUint64(v *hexutil.Uint64) *uint64 {
	if v == nil {
		return nil
	}
	u := uint64(*v)
	return &u
}
