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

package conf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"

	"github.com/n42blockchain/N42/params"
)

type Genesis struct {
	Config      *params.ChainConfig `json:"config" yaml:"config"`
	Nonce       uint64              `json:"nonce"`
	Timestamp   uint64              `json:"timestamp"`
	ExtraData   []byte              `json:"extraData"`
	GasLimit    uint64              `json:"gasLimit"   gencodec:"required"`
	Difficulty  *uint256.Int        `json:"difficulty" gencodec:"required"`
	Mixhash     types.Hash          `json:"mixHash"`
	Coinbase    types.Address       `json:"coinbase"`
	StateRoot   types.Hash          `json:"stateRoot"`
	TxHash      types.Hash          `json:"transactionsTrie"`
	ReceiptHash types.Hash          `json:"receiptTrie"`
	Hash        types.Hash          `json:"hash"`

	Miners []string     `json:"miners" yaml:"miners"`
	Alloc  GenesisAlloc `json:"alloc" yaml:"alloc"  gencodec:"required"`

	Number     uint64       `json:"number"`
	GasUsed    uint64       `json:"gasUsed"`
	ParentHash types.Hash   `json:"parentHash"`
	BaseFee    *uint256.Int `json:"baseFeePerGas"`
}

type GenesisAlloc map[types.Address]GenesisAccount

type GenesisAccount struct {
	Balance string                    `json:"balance"`
	Code    []byte                    `json:"code,omitempty"`
	Storage map[types.Hash]types.Hash `json:"storage,omitempty"`
	Nonce   uint64                    `json:"nonce,omitempty"`
}

type genesisJSON struct {
	Config      *params.ChainConfig `json:"config"`
	Nonce       json.RawMessage     `json:"nonce"`
	Timestamp   json.RawMessage     `json:"timestamp"`
	ExtraData   json.RawMessage     `json:"extraData"`
	GasLimit    json.RawMessage     `json:"gasLimit"`
	Difficulty  json.RawMessage     `json:"difficulty"`
	Mixhash     types.Hash          `json:"mixHash"`
	Coinbase    types.Address       `json:"coinbase"`
	StateRoot   types.Hash          `json:"stateRoot"`
	TxHash      types.Hash          `json:"transactionsTrie"`
	ReceiptHash types.Hash          `json:"receiptTrie"`
	Hash        types.Hash          `json:"hash"`
	Miners      []string            `json:"miners"`
	Alloc       json.RawMessage     `json:"alloc"`
	Number      json.RawMessage     `json:"number"`
	GasUsed     json.RawMessage     `json:"gasUsed"`
	ParentHash  types.Hash          `json:"parentHash"`
	BaseFee     json.RawMessage     `json:"baseFeePerGas"`
}

type genesisAccountJSON struct {
	Balance string            `json:"balance"`
	Code    json.RawMessage   `json:"code,omitempty"`
	Storage map[string]string `json:"storage,omitempty"`
	Nonce   json.RawMessage   `json:"nonce,omitempty"`
}

// UnmarshalJSON accepts both the repository's native genesis format and
// Hive/Geth-style genesis files where numeric and byte fields are hex strings.
func (g *Genesis) UnmarshalJSON(input []byte) error {
	var dec genesisJSON
	if err := json.Unmarshal(input, &dec); err != nil {
		return err
	}

	nonce, err := decodeGenesisUint64("nonce", dec.Nonce)
	if err != nil {
		return err
	}
	timestamp, err := decodeGenesisUint64("timestamp", dec.Timestamp)
	if err != nil {
		return err
	}
	extraData, err := decodeGenesisBytes("extraData", dec.ExtraData)
	if err != nil {
		return err
	}
	gasLimit, err := decodeGenesisUint64("gasLimit", dec.GasLimit)
	if err != nil {
		return err
	}
	difficulty, err := decodeGenesisUint256("difficulty", dec.Difficulty)
	if err != nil {
		return err
	}
	alloc, err := decodeGenesisAlloc(dec.Alloc)
	if err != nil {
		return err
	}
	number, err := decodeGenesisUint64("number", dec.Number)
	if err != nil {
		return err
	}
	gasUsed, err := decodeGenesisUint64("gasUsed", dec.GasUsed)
	if err != nil {
		return err
	}
	baseFee, err := decodeGenesisUint256("baseFeePerGas", dec.BaseFee)
	if err != nil {
		return err
	}

	*g = Genesis{
		Config:      params.NormalizeConsensus(dec.Config),
		Nonce:       nonce,
		Timestamp:   timestamp,
		ExtraData:   extraData,
		GasLimit:    gasLimit,
		Difficulty:  difficulty,
		Mixhash:     dec.Mixhash,
		Coinbase:    dec.Coinbase,
		StateRoot:   dec.StateRoot,
		TxHash:      dec.TxHash,
		ReceiptHash: dec.ReceiptHash,
		Hash:        dec.Hash,
		Miners:      dec.Miners,
		Alloc:       alloc,
		Number:      number,
		GasUsed:     gasUsed,
		ParentHash:  dec.ParentHash,
		BaseFee:     baseFee,
	}
	return nil
}

func decodeGenesisAlloc(input json.RawMessage) (GenesisAlloc, error) {
	if isGenesisJSONNull(input) || len(input) == 0 {
		return nil, nil
	}

	var raw map[string]genesisAccountJSON
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, fmt.Errorf("alloc: %w", err)
	}

	alloc := make(GenesisAlloc, len(raw))
	for key, account := range raw {
		addr, err := decodeGenesisAddress(key)
		if err != nil {
			return nil, fmt.Errorf("alloc[%q]: %w", key, err)
		}
		code, err := decodeGenesisBytes("alloc.code", account.Code)
		if err != nil {
			return nil, fmt.Errorf("alloc[%q]: %w", key, err)
		}
		nonce, err := decodeGenesisUint64("alloc.nonce", account.Nonce)
		if err != nil {
			return nil, fmt.Errorf("alloc[%q]: %w", key, err)
		}
		storage, err := decodeGenesisStorage(account.Storage)
		if err != nil {
			return nil, fmt.Errorf("alloc[%q]: %w", key, err)
		}
		alloc[addr] = GenesisAccount{
			Balance: account.Balance,
			Code:    code,
			Storage: storage,
			Nonce:   nonce,
		}
	}
	return alloc, nil
}

func decodeGenesisStorage(input map[string]string) (map[types.Hash]types.Hash, error) {
	if len(input) == 0 {
		return nil, nil
	}
	storage := make(map[types.Hash]types.Hash, len(input))
	for key, value := range input {
		k, err := decodeGenesisHash(key)
		if err != nil {
			return nil, fmt.Errorf("storage key %q: %w", key, err)
		}
		v, err := decodeGenesisHash(value)
		if err != nil {
			return nil, fmt.Errorf("storage value for %q: %w", key, err)
		}
		storage[k] = v
	}
	return storage, nil
}

func decodeGenesisAddress(input string) (types.Address, error) {
	var addr types.Address
	if input == "" {
		return addr, fmt.Errorf("empty address")
	}
	if !strings.HasPrefix(input, "0x") && !strings.HasPrefix(input, "0X") {
		input = "0x" + input
	}
	if err := addr.UnmarshalText([]byte(input)); err != nil {
		return addr, err
	}
	return addr, nil
}

func decodeGenesisHash(input string) (types.Hash, error) {
	var hash types.Hash
	if input == "" {
		return hash, fmt.Errorf("empty hash")
	}
	if !strings.HasPrefix(input, "0x") && !strings.HasPrefix(input, "0X") {
		input = "0x" + input
	}
	decoded, err := hexutil.Decode(input)
	if err != nil {
		return hash, err
	}
	return types.BytesToHash(decoded), nil
}

func decodeGenesisBytes(field string, input json.RawMessage) ([]byte, error) {
	if isGenesisJSONNull(input) || len(input) == 0 {
		return nil, nil
	}

	var raw hexutil.Bytes
	if err := json.Unmarshal(input, &raw); err == nil {
		return []byte(raw), nil
	}

	var text string
	if err := json.Unmarshal(input, &text); err != nil {
		return nil, fmt.Errorf("%s: expected hex string: %w", field, err)
	}
	if text == "" {
		return nil, nil
	}
	if !strings.HasPrefix(text, "0x") && !strings.HasPrefix(text, "0X") {
		text = "0x" + text
	}
	decoded, err := hexutil.Decode(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return decoded, nil
}

func decodeGenesisUint64(field string, input json.RawMessage) (uint64, error) {
	if isGenesisJSONNull(input) || len(input) == 0 {
		return 0, nil
	}

	var value uint64
	if err := json.Unmarshal(input, &value); err == nil {
		return value, nil
	}

	var text string
	if err := json.Unmarshal(input, &text); err != nil {
		return 0, fmt.Errorf("%s: expected hex/decimal string or number: %w", field, err)
	}
	if text == "" {
		return 0, nil
	}
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		raw := normalizeGenesisHexQuantity(text)[2:]
		value, err := strconv.ParseUint(raw, 16, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", field, err)
		}
		return value, nil
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	return value, nil
}

func decodeGenesisUint256(field string, input json.RawMessage) (*uint256.Int, error) {
	if isGenesisJSONNull(input) || len(input) == 0 {
		return nil, nil
	}

	var text string
	if err := json.Unmarshal(input, &text); err == nil {
		if text == "" {
			return nil, nil
		}
		if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
			value, err := uint256.FromHex(normalizeGenesisHexQuantity(text))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", field, err)
			}
			return value, nil
		}
		value, err := uint256.FromDecimal(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		return value, nil
	}

	var number json.Number
	if err := json.Unmarshal(input, &number); err == nil {
		value, err := uint256.FromDecimal(number.String())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		return value, nil
	}

	return nil, fmt.Errorf("%s: expected hex/decimal string or number", field)
}

func isGenesisJSONNull(input json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(input), []byte("null"))
}

func normalizeGenesisHexQuantity(input string) string {
	raw := strings.TrimLeft(input[2:], "0")
	if raw == "" {
		raw = "0"
	}
	return input[:2] + raw
}
