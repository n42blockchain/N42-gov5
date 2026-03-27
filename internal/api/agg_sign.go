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

package api

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/blst"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

var sigChannel = make(chan AggSign, 10)

var validVerifiers = map[string]string{}

type AggSign struct {
	Number    uint64          `json:"number"`
	StateRoot types.Hash      `json:"stateRoot"`
	Sign      types.Signature `json:"sign"`
	Address   types.Address   `json:"address"`
	PublicKey types.PublicKey `json:"-"`
}

func (s *AggSign) Check(root types.Hash) bool {
	if s.StateRoot != root {
		return false
	}
	sig, err := bls.SignatureFromBytes(s.Sign[:])
	if err != nil {
		return false
	}

	pub, err := bls.PublicKeyFromBytes(s.PublicKey[:])
	if err != nil {
		return false
	}
	return sig.Verify(pub, s.StateRoot[:])
}

func DepositInfo(db kv.RwDB, key types.Address) *deposit.Info {
	var info *deposit.Info
	_ = db.View(context.Background(), func(tx kv.Tx) error {
		info = deposit.GetDepositInfo(tx, key)
		return nil
	})
	return info
}

func IsDeposit(db kv.RwDB, addr types.Address) (bool, error) {
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	return rawdb.IsDeposit(tx, addr), nil
}

func SignMerge(ctx context.Context, header *block.Header, depositNum uint64) (types.Signature, []*block.Verify, error) {
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return types.Signature{}, nil, err
	}
	aggrSigns := make([]bls.Signature, 0)
	verifiers := make([]*block.Verify, 0)
	uniq := make(map[types.Address]struct{})

LOOP:
	for {
		select {
		case s := <-sigChannel:
			log.Tracef("accept sign, %+v", s)
			if s.Number != headerNumber.Uint64() {
				log.Tracef("discard sign: need block number %d, get %d", headerNumber.Uint64(), s.Number)
				continue
			}

			if _, ok := uniq[s.Address]; ok {
				continue
			}

			if !s.Check(header.Root) {
				log.Tracef("discard sign: sign check failed! %v", s)
				continue
			}
			sig, err := bls.SignatureFromBytes(s.Sign[:])
			if err != nil {
				return types.Signature{}, nil, err
			}

			aggrSigns = append(aggrSigns, sig)
			verifiers = append(verifiers, &block.Verify{
				Address:   s.Address,
				PublicKey: s.PublicKey,
			})
			uniq[s.Address] = struct{}{}
		case <-ctx.Done():
			break LOOP
		}
	}
	// TODO: adjust threshold based on depositNum (e.g. depositNum/2)
	if uint64(len(aggrSigns)) < 3 {
		return types.Signature{}, nil, consensus.ErrNotEnoughSign
	}

	aggS := blst.AggregateSignatures(aggrSigns)
	var aggSign types.Signature
	copy(aggSign[:], aggS.Marshal())
	return aggSign, verifiers, nil
}

func MachineVerify(ctx context.Context) error {
	entire := make(chan common.MinedEntireEvent, 10)
	blocksSub, err := event.GlobalEvent.Subscribe(entire)
	if err != nil {
		return fmt.Errorf("failed to subscribe to MinedEntireEvent: %w", err)
	}
	defer blocksSub.Unsubscribe()

	errs := make(chan error, 1)

	for {
		select {
		case b := <-entire:
			// Type assert Entire from interface{} to state.EntireCode
			entireCode, ok := b.Entire.(state.EntireCode)
			if !ok {
				log.Warn("MinedEntireEvent.Entire is not state.EntireCode")
				continue
			}
			headerNumber, err := requireHeaderNumber(entireCode.Entire.Header, "header number unavailable")
			if err != nil {
				log.Warn("MachineVerify skipped entire without block number", "err", err)
				continue
			}
			log.Tracef("machine verify accept entire, number: %d", headerNumber.Uint64())
			for k, s := range validVerifiers {
				go func(seckey string, address string, ec state.EntireCode) {
					defer func() {
						if r := recover(); r != nil {
							log.Errorf("panic in MachineVerify goroutine: %v", r)
						}
					}()

					// recover private key
					sByte, err := hex.DecodeString(seckey)
					if err != nil {
						select {
						case errs <- err:
						default:
						}
						return
					}
					var addr types.Address
					if !addr.DecodeString(address) {
						select {
						case errs <- errors.New("invalid address"):
						default:
						}
						return
					}

					// before state verify
					var hash types.Hash
					hasher := sha3.NewLegacyKeccak256()
					if err = state.EncodeBeforeState(hasher, ec.Entire.Snap.Items, ec.Codes); err != nil {
						select {
						case errs <- err:
						default:
						}
						return
					}
					_, err = hasher.(crypto.KeccakState).Read(hash[:])
					if err != nil {
						return
					}
					eventHeaderNumber, err := requireHeaderNumber(ec.Entire.Header, "header number unavailable")
					if err != nil {
						log.Warn("MachineVerify skipped sign without block number", "err", err)
						return
					}
					if ec.Entire.Header.MixDigest != hash {
						log.Warn("misMatch before state hash", "want:", ec.Entire.Header.MixDigest, "get:", hash, eventHeaderNumber.Uint64())
						return
					}

					// publicKey
					var bs [32]byte
					copy(bs[:], sByte)
					pri, err := bls.SecretKeyFromRandom32Byte(bs)
					if err != nil {
						select {
						case errs <- err:
						default:
						}
						return
					}

					// Signature
					sign := pri.Sign(ec.Entire.Header.Root[:])
					tmp := AggSign{Number: eventHeaderNumber.Uint64()}
					copy(tmp.StateRoot[:], ec.Entire.Header.Root[:])
					copy(tmp.Sign[:], sign.Marshal())
					copy(tmp.PublicKey[:], pri.PublicKey().Marshal())
					tmp.Address = addr
					select {
					case sigChannel <- tmp:
					case <-ctx.Done():
						log.Debug("MachineVerify: context cancelled, dropping sign", "number", tmp.Number)
					}
				}(s, k, entireCode)
			}
		case <-ctx.Done():
			return nil
		case err := <-errs:
			return err
		}
	}
}
