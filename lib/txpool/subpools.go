/*
   Copyright 2022 The Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package txpool

import (
	"container/heap"
	"fmt"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/log/v3"
)

// better returns true if mt should be ranked higher than "than" in the sub-pool ordering.
func (mt *metaTx) better(than *metaTx, pendingBaseFee uint256.Int) bool {
	subPool := mt.subPool
	thanSubPool := than.subPool
	if mt.minFeeCap.Cmp(&pendingBaseFee) >= 0 {
		subPool |= EnoughFeeCapBlock
	}
	if than.minFeeCap.Cmp(&pendingBaseFee) >= 0 {
		thanSubPool |= EnoughFeeCapBlock
	}
	if subPool != thanSubPool {
		return subPool > thanSubPool
	}

	switch mt.currentSubPool {
	case PendingSubPool:
		var effectiveTip, thanEffectiveTip uint256.Int
		if mt.minFeeCap.Cmp(&pendingBaseFee) >= 0 {
			difference := uint256.NewInt(0)
			difference.Sub(&mt.minFeeCap, &pendingBaseFee)
			if difference.Cmp(uint256.NewInt(mt.minTip)) <= 0 {
				effectiveTip = *difference
			} else {
				effectiveTip = *uint256.NewInt(mt.minTip)
			}
		}
		if than.minFeeCap.Cmp(&pendingBaseFee) >= 0 {
			difference := uint256.NewInt(0)
			difference.Sub(&than.minFeeCap, &pendingBaseFee)
			if difference.Cmp(uint256.NewInt(than.minTip)) <= 0 {
				thanEffectiveTip = *difference
			} else {
				thanEffectiveTip = *uint256.NewInt(than.minTip)
			}
		}
		if effectiveTip.Cmp(&thanEffectiveTip) != 0 {
			return effectiveTip.Cmp(&thanEffectiveTip) > 0
		}
		// Compare nonce and cumulative balance. Just as a side note, it doesn't
		// matter if they're from same sender or not because we're comparing
		// nonce distance of the sender from state's nonce and not the actual
		// value of nonce.
		if mt.nonceDistance != than.nonceDistance {
			return mt.nonceDistance < than.nonceDistance
		}
		if mt.cumulativeBalanceDistance != than.cumulativeBalanceDistance {
			return mt.cumulativeBalanceDistance < than.cumulativeBalanceDistance
		}
	case BaseFeeSubPool:
		if mt.minFeeCap.Cmp(&than.minFeeCap) != 0 {
			return mt.minFeeCap.Cmp(&than.minFeeCap) > 0
		}
	case QueuedSubPool:
		if mt.nonceDistance != than.nonceDistance {
			return mt.nonceDistance < than.nonceDistance
		}
		if mt.cumulativeBalanceDistance != than.cumulativeBalanceDistance {
			return mt.cumulativeBalanceDistance < than.cumulativeBalanceDistance
		}
	}
	return mt.timestamp < than.timestamp
}

func (mt *metaTx) worse(than *metaTx, pendingBaseFee uint256.Int) bool {
	subPool := mt.subPool
	thanSubPool := than.subPool
	if mt.minFeeCap.Cmp(&pendingBaseFee) >= 0 {
		subPool |= EnoughFeeCapBlock
	}
	if than.minFeeCap.Cmp(&pendingBaseFee) >= 0 {
		thanSubPool |= EnoughFeeCapBlock
	}
	if subPool != thanSubPool {
		return subPool < thanSubPool
	}

	switch mt.currentSubPool {
	case PendingSubPool:
		if mt.minFeeCap != than.minFeeCap {
			return mt.minFeeCap.Cmp(&than.minFeeCap) < 0
		}
		if mt.nonceDistance != than.nonceDistance {
			return mt.nonceDistance > than.nonceDistance
		}
		if mt.cumulativeBalanceDistance != than.cumulativeBalanceDistance {
			return mt.cumulativeBalanceDistance > than.cumulativeBalanceDistance
		}
	case BaseFeeSubPool, QueuedSubPool:
		if mt.nonceDistance != than.nonceDistance {
			return mt.nonceDistance > than.nonceDistance
		}
		if mt.cumulativeBalanceDistance != than.cumulativeBalanceDistance {
			return mt.cumulativeBalanceDistance > than.cumulativeBalanceDistance
		}
	}
	return mt.timestamp > than.timestamp
}

// PendingPool uses a sorted slice (not heap) for best, enabling cheap copies for mining.
type PendingPool struct {
	best  *bestSlice
	worst *WorstQueue
	limit int
	t     SubPoolType
}

func NewPendingSubPool(t SubPoolType, limit int) *PendingPool {
	return &PendingPool{limit: limit, t: t, best: &bestSlice{ms: []*metaTx{}}, worst: &WorstQueue{ms: []*metaTx{}}}
}

// bestSlice - is similar to best queue, but uses a linear structure with O(n log n) sort complexity and
// it maintains element.bestIndex field
type bestSlice struct {
	ms             []*metaTx
	pendingBaseFee uint64
}

func (s *bestSlice) Len() int { return len(s.ms) }
func (s *bestSlice) Swap(i, j int) {
	s.ms[i], s.ms[j] = s.ms[j], s.ms[i]
	s.ms[i].bestIndex, s.ms[j].bestIndex = i, j
}
func (s *bestSlice) Less(i, j int) bool {
	return s.ms[i].better(s.ms[j], *uint256.NewInt(s.pendingBaseFee))
}
func (s *bestSlice) UnsafeRemove(i *metaTx) {
	s.Swap(i.bestIndex, len(s.ms)-1)
	s.ms[len(s.ms)-1].bestIndex = -1
	s.ms[len(s.ms)-1] = nil
	s.ms = s.ms[:len(s.ms)-1]
}
func (s *bestSlice) UnsafeAdd(i *metaTx) {
	i.bestIndex = len(s.ms)
	s.ms = append(s.ms, i)
}

func (p *PendingPool) EnforceWorstInvariants() {
	heap.Init(p.worst)
}
func (p *PendingPool) EnforceBestInvariants() {
	sort.Sort(p.best)
}

func (p *PendingPool) Best() *metaTx { //nolint
	if len(p.best.ms) == 0 {
		return nil
	}
	return p.best.ms[0]
}
func (p *PendingPool) Worst() *metaTx { //nolint
	if len(p.worst.ms) == 0 {
		return nil
	}
	return (p.worst.ms)[0]
}
func (p *PendingPool) PopWorst() *metaTx { //nolint
	i := heap.Pop(p.worst).(*metaTx)
	if i.bestIndex >= 0 {
		p.best.UnsafeRemove(i)
	}
	return i
}
func (p *PendingPool) Updated(mt *metaTx) {
	heap.Fix(p.worst, mt.worstIndex)
}
func (p *PendingPool) Len() int { return len(p.best.ms) }

func (p *PendingPool) Remove(i *metaTx, reason string, logger log.Logger) {
	if i.Tx.Traced {
		logger.Info(fmt.Sprintf("TX TRACING: removed from subpool %s", p.t), "idHash", fmt.Sprintf("%x", i.Tx.IDHash), "sender", i.Tx.SenderID, "nonce", i.Tx.Nonce, "reason", reason)
	}
	if i.worstIndex >= 0 {
		heap.Remove(p.worst, i.worstIndex)
	}
	if i.bestIndex >= 0 {
		p.best.UnsafeRemove(i)
	}
	i.currentSubPool = 0
}

func (p *PendingPool) Add(i *metaTx, logger log.Logger) {
	if i.Tx.Traced {
		logger.Info(fmt.Sprintf("TX TRACING: added to subpool %s, IdHash=%x, sender=%d, nonce=%d", p.t, i.Tx.IDHash, i.Tx.SenderID, i.Tx.Nonce))
	}
	i.currentSubPool = p.t
	heap.Push(p.worst, i)
	p.best.UnsafeAdd(i)
}
func (p *PendingPool) DebugPrint(prefix string) {
	for i, it := range p.best.ms {
		fmt.Printf("%s.best: %d, %d, %d,%d\n", prefix, i, it.subPool, it.bestIndex, it.Tx.Nonce)
	}
	for i, it := range p.worst.ms {
		fmt.Printf("%s.worst: %d, %d, %d,%d\n", prefix, i, it.subPool, it.worstIndex, it.Tx.Nonce)
	}
}

type SubPool struct {
	best  *BestQueue
	worst *WorstQueue
	limit int
	t     SubPoolType
}

func NewSubPool(t SubPoolType, limit int) *SubPool {
	return &SubPool{limit: limit, t: t, best: &BestQueue{}, worst: &WorstQueue{}}
}

func (p *SubPool) EnforceInvariants() {
	heap.Init(p.worst)
	heap.Init(p.best)
}
func (p *SubPool) Best() *metaTx { //nolint
	if len(p.best.ms) == 0 {
		return nil
	}
	return p.best.ms[0]
}
func (p *SubPool) Worst() *metaTx { //nolint
	if len(p.worst.ms) == 0 {
		return nil
	}
	return p.worst.ms[0]
}
func (p *SubPool) PopBest() *metaTx { //nolint
	i := heap.Pop(p.best).(*metaTx)
	heap.Remove(p.worst, i.worstIndex)
	return i
}
func (p *SubPool) PopWorst() *metaTx { //nolint
	i := heap.Pop(p.worst).(*metaTx)
	heap.Remove(p.best, i.bestIndex)
	return i
}
func (p *SubPool) Len() int { return p.best.Len() }
func (p *SubPool) Add(i *metaTx, reason string, logger log.Logger) {
	if i.Tx.Traced {
		logger.Info(fmt.Sprintf("TX TRACING: added to subpool %s", p.t), "idHash", fmt.Sprintf("%x", i.Tx.IDHash), "sender", i.Tx.SenderID, "nonce", i.Tx.Nonce, "reason", reason)
	}
	i.currentSubPool = p.t
	heap.Push(p.best, i)
	heap.Push(p.worst, i)
}

func (p *SubPool) Remove(i *metaTx, reason string, logger log.Logger) {
	if i.Tx.Traced {
		logger.Info(fmt.Sprintf("TX TRACING: removed from subpool %s", p.t), "idHash", fmt.Sprintf("%x", i.Tx.IDHash), "sender", i.Tx.SenderID, "nonce", i.Tx.Nonce, "reason", reason)
	}
	heap.Remove(p.best, i.bestIndex)
	heap.Remove(p.worst, i.worstIndex)
	i.currentSubPool = 0
}

func (p *SubPool) Updated(i *metaTx) {
	heap.Fix(p.best, i.bestIndex)
	heap.Fix(p.worst, i.worstIndex)
}

func (p *SubPool) DebugPrint(prefix string) {
	for i, it := range p.best.ms {
		fmt.Printf("%s.best: %d, %d, %d\n", prefix, i, it.subPool, it.bestIndex)
	}
	for i, it := range p.worst.ms {
		fmt.Printf("%s.worst: %d, %d, %d\n", prefix, i, it.subPool, it.worstIndex)
	}
}

type BestQueue struct {
	ms             []*metaTx
	pendingBastFee uint64
}

func (p BestQueue) Len() int { return len(p.ms) }
func (p BestQueue) Less(i, j int) bool {
	return p.ms[i].better(p.ms[j], *uint256.NewInt(p.pendingBastFee))
}
func (p BestQueue) Swap(i, j int) {
	p.ms[i], p.ms[j] = p.ms[j], p.ms[i]
	p.ms[i].bestIndex = i
	p.ms[j].bestIndex = j
}
func (p *BestQueue) Push(x interface{}) {
	n := len(p.ms)
	item := x.(*metaTx)
	item.bestIndex = n
	p.ms = append(p.ms, item)
}

func (p *BestQueue) Pop() interface{} {
	old := p.ms
	n := len(old)
	item := old[n-1]
	old[n-1] = nil          // avoid memory leak
	item.bestIndex = -1     // for safety
	item.currentSubPool = 0 // for safety
	p.ms = old[0 : n-1]
	return item
}

type WorstQueue struct {
	ms             []*metaTx
	pendingBaseFee uint64
}

func (p WorstQueue) Len() int { return len(p.ms) }
func (p WorstQueue) Less(i, j int) bool {
	return p.ms[i].worse(p.ms[j], *uint256.NewInt(p.pendingBaseFee))
}
func (p WorstQueue) Swap(i, j int) {
	p.ms[i], p.ms[j] = p.ms[j], p.ms[i]
	p.ms[i].worstIndex = i
	p.ms[j].worstIndex = j
}
func (p *WorstQueue) Push(x interface{}) {
	item := x.(*metaTx)
	item.worstIndex = len(p.ms)
	p.ms = append(p.ms, item)
}
func (p *WorstQueue) Pop() interface{} {
	old := p.ms
	n := len(old)
	item := old[n-1]
	old[n-1] = nil          // avoid memory leak
	item.worstIndex = -1    // for safety
	item.currentSubPool = 0 // for safety
	p.ms = old[0 : n-1]
	return item
}
