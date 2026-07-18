package initialsync

import (
	"context"

	"github.com/holiman/uint256"
)

// resetFromBlockNr removes all state machines, and re-adds them starting with a given BlockNr.
func (q *blocksQueue) resetFromBlockNr(ctx context.Context, startBlockNr *uint256.Int) error {
	// Shift start position of all the machines except for the last one.
	blocksPerRequest := q.blocksFetcher.blocksPerPeriod
	if err := q.smm.removeAllStateMachines(); err != nil {
		return err
	}
	for i := startBlockNr.Clone(); i.Cmp(new(uint256.Int).AddUint64(startBlockNr, blocksPerRequest*(lookaheadSteps-1))) == -1; i.AddUint64(i, blocksPerRequest) {
		q.smm.addStateMachine(i)
	}

	return nil
}
