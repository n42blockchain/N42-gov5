package internal

import (
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

type executionMessagePolicy struct {
	checkNonce                    bool
	zeroFeeRequiresPaidAccounting bool
}

func newExecutionMessagePolicy(statelessExec bool, hasConsensusEngine bool) executionMessagePolicy {
	return executionMessagePolicy{
		checkNonce:                    !statelessExec,
		zeroFeeRequiresPaidAccounting: hasConsensusEngine,
	}
}

// NormalizeExecutionMessage applies the shared execution-path message defaults.
func NormalizeExecutionMessage(msg *transaction.Message, statelessExec bool, hasConsensusEngine bool) {
	newExecutionMessagePolicy(statelessExec, hasConsensusEngine).normalize(msg)
}

func (p executionMessagePolicy) normalize(msg *transaction.Message) {
	if msg == nil {
		return
	}
	msg.SetCheckNonce(p.checkNonce)
	if p.zeroFeeRequiresPaidAccounting && msg.FeeCap().IsZero() {
		msg.SetIsFree(false)
	}
}

// ApplyServiceTransactionPolicy marks zero-fee messages as free when the
// consensus engine classifies the sender as a service transaction.
func ApplyServiceTransactionPolicy(msg *transaction.Message, isService func(types.Address) bool) {
	if msg == nil || isService == nil || !msg.FeeCap().IsZero() {
		return
	}
	msg.SetIsFree(isService(msg.From()))
}
