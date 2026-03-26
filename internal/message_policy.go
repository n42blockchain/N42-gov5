package internal

import (
	"github.com/n42blockchain/N42/common/transaction"
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

func (p executionMessagePolicy) normalize(msg *transaction.Message) {
	if msg == nil {
		return
	}
	msg.SetCheckNonce(p.checkNonce)
	if p.zeroFeeRequiresPaidAccounting && msg.FeeCap().IsZero() {
		msg.SetIsFree(false)
	}
}
