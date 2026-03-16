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

package deposit

import (
	"context"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/crypto/bls"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

const (
	DayPerMonth = 30

	// AMT deposit tiers (in N tokens)
	fiftyDeposit       = 50
	OneHundredDeposit  = 100
	FiveHundredDeposit = 500

	// FUJI deposit tiers (in N tokens)
	fuji2000Deposit = 2000
	fuji800Deposit  = 800
	fuji200Deposit  = 200

	// Maximum tasks per epoch by AMT deposit tier
	fiftyDepositMaxTaskPerEpoch       = 500
	OneHundredDepositMaxTaskPerEpoch  = 100
	FiveHundredDepositMaxTaskPerEpoch = 100

	// Maximum tasks per epoch for FUJI deposits
	fujiMaxTaskPerEpoch = 50

	// Monthly rewards by AMT deposit tier
	fiftyDepositRewardPerMonth       = 0.375 * params.N
	OneHundredDepositRewardPerMonth  = 1 * params.N
	FiveHundredDepositRewardPerMonth = 6.25 * params.N // max uint64 = ^uint64(0) ~ 18.44 N, so 6.25 N is safe

	// Rewards for FUJI deposit tiers
	fuji200RewardPerEpoch  = 0.025 * params.N
	fuji800RewardPerEpoch  = 0.1 * params.N
	fuji2000RewardPerMonth = 10 * params.N
)

// DepositContract defines the interface for interacting with deposit contracts.
type DepositContract interface {
	WithdrawnSignature() types.Hash
	DepositSignature() types.Hash
	UnpackDepositLogData(data []byte) (publicKey []byte, signature []byte, depositAmount *uint256.Int, err error)
	IsDepositAction(sig [4]byte) bool
}

func GetDepositInfo(tx kv.Tx, addr types.Address) *Info {
	pubkey, depositAmount, err := rawdb.GetDeposit(tx, addr)
	if err != nil {
		return nil
	}

	var (
		maxRewardPerEpoch *uint256.Int
		rewardPerBlock    *uint256.Int
	)
	depositEther := new(uint256.Int).Div(depositAmount, uint256.NewInt(params.N)).Uint64()
	switch depositEther {
	case fiftyDeposit:
		rewardPerBlock = new(uint256.Int).Div(uint256.NewInt(fiftyDepositRewardPerMonth), uint256.NewInt(DayPerMonth*fiftyDepositMaxTaskPerEpoch))
		maxRewardPerEpoch = new(uint256.Int).Mul(rewardPerBlock, uint256.NewInt(fiftyDepositMaxTaskPerEpoch))

	case OneHundredDeposit:
		rewardPerBlock = new(uint256.Int).Div(uint256.NewInt(OneHundredDepositRewardPerMonth), uint256.NewInt(DayPerMonth*OneHundredDepositMaxTaskPerEpoch))
		rewardPerBlock = new(uint256.Int).Add(rewardPerBlock, uint256.NewInt(params.Wei))
		maxRewardPerEpoch = new(uint256.Int).Mul(rewardPerBlock, uint256.NewInt(OneHundredDepositMaxTaskPerEpoch))

	case FiveHundredDeposit:
		rewardPerBlock = new(uint256.Int).Div(uint256.NewInt(FiveHundredDepositRewardPerMonth), uint256.NewInt(DayPerMonth*FiveHundredDepositMaxTaskPerEpoch))
		rewardPerBlock = new(uint256.Int).Add(rewardPerBlock, uint256.NewInt(params.Wei))
		maxRewardPerEpoch = new(uint256.Int).Mul(rewardPerBlock, uint256.NewInt(FiveHundredDepositMaxTaskPerEpoch))

	case fuji200Deposit:
		rewardPerBlock = new(uint256.Int).Div(uint256.NewInt(fuji200RewardPerEpoch), uint256.NewInt(fujiMaxTaskPerEpoch))
		maxRewardPerEpoch = new(uint256.Int).SetUint64(fuji200RewardPerEpoch)

	case fuji800Deposit:
		rewardPerBlock = new(uint256.Int).Div(uint256.NewInt(fuji800RewardPerEpoch), uint256.NewInt(fujiMaxTaskPerEpoch))
		maxRewardPerEpoch = new(uint256.Int).SetUint64(fuji800RewardPerEpoch)

	case fuji2000Deposit:
		rewardPerBlock = new(uint256.Int).Div(uint256.NewInt(fuji2000RewardPerMonth), uint256.NewInt(DayPerMonth*fujiMaxTaskPerEpoch))
		rewardPerBlock = new(uint256.Int).Add(rewardPerBlock, uint256.NewInt(params.Wei))
		maxRewardPerEpoch = new(uint256.Int).Mul(rewardPerBlock, uint256.NewInt(fujiMaxTaskPerEpoch))

	case 10:
		// 10 deposit is reserved for testing purposes only
		return nil

	default:
		log.Warn("unknown deposit amount, skipping", "depositEther", depositEther, "addr", addr)
		return nil
	}

	return &Info{
		PublicKey:         pubkey,
		DepositAmount:     depositAmount,
		RewardPerBlock:    rewardPerBlock,
		MaxRewardPerEpoch: maxRewardPerEpoch,
	}
}

type Info struct {
	PublicKey         types.PublicKey `json:"PublicKey"`
	DepositAmount     *uint256.Int    `json:"DepositAmount"`
	RewardPerBlock    *uint256.Int    `json:"RewardPerBlock"`
	MaxRewardPerEpoch *uint256.Int    `json:"MaxRewardPerEpoch"`
}

type Deposit struct {
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	blockChain common.IBlockChain
	db         kv.RwDB

	logsSub   event.Subscription // Subscription for new log event
	rmLogsSub event.Subscription // Subscription for removed log event

	logsCh   chan common.NewLogsEvent     // Channel to receive new log event
	rmLogsCh chan common.RemovedLogsEvent // Channel to receive removed log event

	depositContracts map[types.Address]DepositContract
}

func NewDeposit(ctx context.Context, bc common.IBlockChain, db kv.RwDB, depositContracts map[types.Address]DepositContract) *Deposit {
	c, cancel := context.WithCancel(ctx)
	d := &Deposit{
		ctx:              c,
		cancel:           cancel,
		blockChain:       bc,
		db:               db,
		logsCh:           make(chan common.NewLogsEvent, 10),
		rmLogsCh:         make(chan common.RemovedLogsEvent, 10),
		depositContracts: depositContracts,
	}

	var logsErr error
	d.logsSub, logsErr = event.GlobalEvent.Subscribe(d.logsCh)
	var removedLogsErr error
	d.rmLogsSub, removedLogsErr = event.GlobalEvent.Subscribe(d.rmLogsCh)

	if d.logsSub == nil || d.rmLogsSub == nil {
		if d.logsSub != nil {
			d.logsSub.Unsubscribe()
			d.logsSub = nil
		}
		if d.rmLogsSub != nil {
			d.rmLogsSub.Unsubscribe()
			d.rmLogsSub = nil
		}
		log.Error("Subscribe for event system failed", "logsErr", logsErr, "removedLogsErr", removedLogsErr)
	}
	return d
}

func (d *Deposit) Start() {
	if d.logsSub == nil || d.rmLogsSub == nil {
		log.Warn("Deposit event listener not started because event subscriptions are unavailable")
		return
	}
	d.wg.Add(1)
	go d.eventLoop()
}

func (d *Deposit) Stop() error {
	d.cancel()
	d.wg.Wait()
	return nil
}

func (d *Deposit) IsDepositAction(txs *transaction.Transaction) bool {
	to := txs.To()
	if to == nil {
		return false
	}

	depositContract, found := d.depositContracts[*to]
	if !found {
		return false
	}

	if len(txs.Data()) < 4 {
		return false
	}

	var sig [4]byte
	copy(sig[:], txs.Data()[:4])
	return depositContract.IsDepositAction(sig)
}

func (d *Deposit) eventLoop() {
	// Ensure all subscriptions get cleaned up
	defer func() {
		d.logsSub.Unsubscribe()
		d.rmLogsSub.Unsubscribe()
		d.wg.Done()
		log.Info("Context closed, exiting goroutine (eventLoop)")
	}()

	for {
		select {
		case logEvent := <-d.logsCh:
			for _, l := range logEvent.Logs {
				if depositContract, found := d.depositContracts[l.Address]; found {
					if l.Topics[0] == depositContract.DepositSignature() {
						d.handleDepositEvent(l.TxHash, l.Data, depositContract)
					} else if l.Topics[0] == depositContract.WithdrawnSignature() {
						d.handleWithdrawnEvent(l.TxHash, l.Data)
					}
				}
			}
		case logRemovedEvent := <-d.rmLogsCh:
			for _, l := range logRemovedEvent.Logs {
				log.Info("removed log event", "address", l.Address, "data", l.Data)
			}
		case <-d.logsSub.Err():
			return
		case <-d.rmLogsSub.Err():
			return
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *Deposit) handleDepositEvent(txHash types.Hash, data []byte, depositContract DepositContract) {
	pb, sig, amount, err := depositContract.UnpackDepositLogData(data)
	if err != nil {
		log.Warn("cannot unpack deposit log data", "err", err)
		return
	}

	signature, err := bls.SignatureFromBytes(sig)
	if err != nil {
		log.Warn("cannot unpack BLS signature", "signature", hexutil.Encode(sig), "err", err)
		return
	}

	publicKey, err := bls.PublicKeyFromBytes(pb)
	if err != nil {
		log.Warn("cannot unpack BLS publicKey", "publicKey", hexutil.Encode(pb), "err", err)
		return
	}

	log.Trace("DepositEvent verify", "signature", hexutil.Encode(signature.Marshal()), "publicKey", hexutil.Encode(publicKey.Marshal()), "msg", hexutil.Encode(amount.Bytes()))

	if !signature.Verify(publicKey, amount.Bytes()) {
		log.Error("DepositEvent cannot verify signature", "signature", hexutil.Encode(sig), "publicKey", hexutil.Encode(pb), "message", hexutil.Encode(amount.Bytes()))
		return
	}

	rwTx, err := d.db.BeginRw(d.ctx)
	if err != nil {
		log.Error("cannot open db", "err", err)
		return
	}
	defer rwTx.Rollback()

	tx, _, _, _, err := rawdb.ReadTransactionByHash(rwTx, txHash)
	if err != nil {
		log.Error("rawdb.ReadTransactionByHash", "err", err, "hash", txHash)
		return
	}
	if tx == nil {
		return
	}

	from := tx.From()
	if from == nil {
		log.Error("deposit tx has nil From address", "hash", txHash)
		return
	}

	log.Info("add Deposit info", "address", from, "amount", amount.String())

	var pub types.PublicKey
	pub.SetBytes(publicKey.Marshal())
	rawdb.PutDeposit(rwTx, *from, pub, *amount)
	if err := rwTx.Commit(); err != nil {
		log.Error("failed to commit deposit", "from", from, "err", err)
	}
}

func (d *Deposit) handleWithdrawnEvent(txHash types.Hash, data []byte) {
	var tx *transaction.Transaction

	rwTx, err := d.db.BeginRw(d.ctx)
	if err != nil {
		log.Error("cannot open db", "err", err)
		return
	}
	defer rwTx.Rollback()

	tx, _, _, _, err = rawdb.ReadTransactionByHash(rwTx, txHash)
	if err != nil {
		log.Error("rawdb.ReadTransactionByHash", "err", err, "hash", txHash)
		return
	}
	if tx == nil {
		log.Error("cannot find Transaction", "err", err, "hash", txHash)
		return
	}

	from := tx.From()
	if from == nil {
		log.Error("withdrawn tx has nil From address", "hash", txHash)
		return
	}

	err = rawdb.DeleteDeposit(rwTx, *from)
	if err != nil {
		log.Error("cannot delete deposit", "err", err)
		return
	}
	if err = rwTx.Commit(); err != nil {
		log.Error("failed to commit withdrawal", "from", from, "err", err)
	}
}
