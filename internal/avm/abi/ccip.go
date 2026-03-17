package abi

import (
	"bytes"
	"fmt"

	common "github.com/n42blockchain/N42/internal/avm/common"
)

var (
	offchainLookupError = NewError("OffchainLookup", Arguments{
		{Name: "sender", Type: mustNewType("address")},
		{Name: "urls", Type: mustNewType("string[]")},
		{Name: "callData", Type: mustNewType("bytes")},
		{Name: "callbackFunction", Type: mustNewType("bytes4")},
		{Name: "extraData", Type: mustNewType("bytes")},
	})
	callbackResponseArgs = Arguments{
		{Name: "response", Type: mustNewType("bytes")},
		{Name: "extraData", Type: mustNewType("bytes")},
	}
)

// OffchainLookup contains the decoded payload from the EIP-3668 error.
type OffchainLookup struct {
	Sender           common.Address
	URLs             []string
	CallData         []byte
	CallbackFunction [4]byte
	ExtraData        []byte
}

func mustNewType(kind string) Type {
	typ, err := NewType(kind, "", nil)
	if err != nil {
		panic(err)
	}
	return typ
}

// IsOffchainLookup reports whether the provided revert data matches the
// standard EIP-3668 OffchainLookup error selector.
func IsOffchainLookup(data []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:4], offchainLookupError.ID[:4])
}

// UnpackOffchainLookup decodes the standard OffchainLookup error payload.
func UnpackOffchainLookup(data []byte) (*OffchainLookup, error) {
	unpacked, err := offchainLookupError.Unpack(data)
	if err != nil {
		return nil, err
	}
	values, ok := unpacked.([]interface{})
	if !ok || len(values) != 5 {
		return nil, fmt.Errorf("unexpected offchain lookup payload: %T", unpacked)
	}
	sender, ok := values[0].(common.Address)
	if !ok {
		return nil, fmt.Errorf("unexpected sender type: %T", values[0])
	}
	urls, ok := values[1].([]string)
	if !ok {
		return nil, fmt.Errorf("unexpected urls type: %T", values[1])
	}
	callData, ok := values[2].([]byte)
	if !ok {
		return nil, fmt.Errorf("unexpected callData type: %T", values[2])
	}
	callbackFunction, ok := values[3].([4]byte)
	if !ok {
		return nil, fmt.Errorf("unexpected callbackFunction type: %T", values[3])
	}
	extraData, ok := values[4].([]byte)
	if !ok {
		return nil, fmt.Errorf("unexpected extraData type: %T", values[4])
	}
	return &OffchainLookup{
		Sender:           sender,
		URLs:             urls,
		CallData:         callData,
		CallbackFunction: callbackFunction,
		ExtraData:        extraData,
	}, nil
}

// PackOffchainLookupResponse encodes the callback parameters required by
// EIP-3668: response bytes from the gateway and the original extraData blob.
func PackOffchainLookupResponse(response []byte, extraData []byte) ([]byte, error) {
	return callbackResponseArgs.Pack(response, extraData)
}
