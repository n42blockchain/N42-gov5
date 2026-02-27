// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package diagnostics

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/lib/log/v3"
)

type PeerStats struct {
	peersInfo     *sync.Map
	recordsCount  int
	lastUpdateMap map[string]time.Time
	limit         int
	mu            sync.Mutex
}

func NewPeerStats(peerLimit int) *PeerStats {
	return &PeerStats{
		peersInfo:     &sync.Map{},
		lastUpdateMap: make(map[string]time.Time),
		limit:         peerLimit,
	}
}

func (p *PeerStats) AddOrUpdatePeer(peerID string, peerInfo PeerStatisticMsgUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.addOrUpdatePeer(peerID, peerInfo)
}

func (p *PeerStats) addOrUpdatePeer(peerID string, peerInfo PeerStatisticMsgUpdate) {
	if value, ok := p.peersInfo.Load(peerID); ok {
		p.updatePeer(peerID, peerInfo, value)
	} else {
		p.addPeer(peerID, peerInfo)
		if p.recordsCount > p.limit {
			p.removePeersWhichExceedLimit(p.limit)
		}
	}
}

// Deprecated - used in tests. non-thread-safe
func (p *PeerStats) AddPeer(peerID string, peerInfo PeerStatisticMsgUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.addPeer(peerID, peerInfo)
}

func (p *PeerStats) addPeer(peerID string, peerInfo PeerStatisticMsgUpdate) {
	pv := peerStatisticsFromMsgUpdate(peerInfo, nil)
	p.peersInfo.Store(peerID, pv)
	p.recordsCount++
	p.lastUpdateMap[peerID] = time.Now()
}

func (p *PeerStats) UpdatePeer(peerID string, peerInfo PeerStatisticMsgUpdate, prevValue any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updatePeer(peerID, peerInfo, prevValue)
}

func (p *PeerStats) updatePeer(peerID string, peerInfo PeerStatisticMsgUpdate, prevValue any) {
	pv := peerStatisticsFromMsgUpdate(peerInfo, prevValue)
	p.peersInfo.Store(peerID, pv)
	p.lastUpdateMap[peerID] = time.Now()
}

func PeerStatisticsFromMsgUpdate(msg PeerStatisticMsgUpdate, prevValue any) PeerStatistics {
	return peerStatisticsFromMsgUpdate(msg, prevValue)
}

func peerStatisticsFromMsgUpdate(msg PeerStatisticMsgUpdate, prevValue any) PeerStatistics {
	ps := PeerStatistics{
		PeerType:     msg.PeerType,
		CapBytesIn:   make(map[string]uint64),
		CapBytesOut:  make(map[string]uint64),
		TypeBytesIn:  make(map[string]uint64),
		TypeBytesOut: make(map[string]uint64),
	}

	// Carry over previous statistics if available
	prev, _ := prevValue.(PeerStatistics)

	bytes := uint64(msg.Bytes)
	if msg.Inbound {
		ps.BytesIn = prev.BytesIn + bytes
		ps.CapBytesIn[msg.MsgCap] = prev.CapBytesIn[msg.MsgCap] + bytes
		ps.TypeBytesIn[msg.MsgType] = prev.TypeBytesIn[msg.MsgType] + bytes
	} else {
		ps.BytesOut = prev.BytesOut + bytes
		ps.CapBytesOut[msg.MsgCap] = prev.CapBytesOut[msg.MsgCap] + bytes
		ps.TypeBytesOut[msg.MsgType] = prev.TypeBytesOut[msg.MsgType] + bytes
	}

	return ps
}

func (p *PeerStats) GetPeersCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.recordsCount
}

func (p *PeerStats) GetPeers() map[string]PeerStatistics {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.getPeers()
}

func (p *PeerStats) getPeers() map[string]PeerStatistics {
	stats := make(map[string]PeerStatistics)
	p.peersInfo.Range(func(key, value interface{}) bool {
		loadedKey, ok := key.(string)
		if !ok {
			log.Debug("Failed to cast key to string", key)
			return true
		}

		loadedValue, ok := value.(PeerStatistics)
		if !ok {
			log.Debug("Failed to cast value to PeerStatistics struct", value)
			return true
		}

		stats[loadedKey] = loadedValue.Clone()
		return true
	})

	return stats
}

func (p *PeerStats) GetPeerStatistics(peerID string) PeerStatistics {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.getPeerStatistics(peerID)
}

func (p *PeerStats) getPeerStatistics(peerID string) PeerStatistics {
	if value, ok := p.peersInfo.Load(peerID); ok {
		if peerStats, ok := value.(PeerStatistics); ok {
			return peerStats.Clone()
		}
	}

	return PeerStatistics{}
}

func (p *PeerStats) RemovePeer(peerID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.removePeer(peerID)
}

func (p *PeerStats) removePeer(peerID string) {
	p.peersInfo.Delete(peerID)
	p.recordsCount--
	delete(p.lastUpdateMap, peerID)
}

type PeerUpdTime struct {
	PeerID string
	Time   time.Time
}

func (p *PeerStats) GetOldestUpdatedPeersWithSize(size int) []PeerUpdTime {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.getOldestUpdatedPeersWithSize(size)
}

func (p *PeerStats) getOldestUpdatedPeersWithSize(size int) []PeerUpdTime {
	timeArray := make([]PeerUpdTime, 0, p.recordsCount)
	for k, v := range p.lastUpdateMap {
		timeArray = append(timeArray, PeerUpdTime{k, v})
	}

	sort.Slice(timeArray, func(i, j int) bool {
		return timeArray[i].Time.Before(timeArray[j].Time)
	})

	if len(timeArray) < size {
		return timeArray
	}
	return timeArray[:size]
}

func (p *PeerStats) RemovePeersWhichExceedLimit(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removePeersWhichExceedLimit(limit)
}

func (p *PeerStats) removePeersWhichExceedLimit(limit int) {
	peersToRemove := p.recordsCount - limit
	if peersToRemove > 0 {
		peers := p.getOldestUpdatedPeersWithSize(peersToRemove)
		for _, peer := range peers {
			p.removePeer(peer.PeerID)
		}
	}
}

func (d *DiagnosticClient) runCollectPeersStatistics(rootCtx context.Context) {
	go func() {
		ctx, ch, closeChannel := Context[PeerStatisticMsgUpdate](rootCtx, 1)
		defer closeChannel()

		StartProviders(ctx, TypeOf(PeerStatisticMsgUpdate{}), log.Root())
		for {
			select {
			case <-rootCtx.Done():
				return
			case info := <-ch:
				d.peersStats.AddOrUpdatePeer(info.PeerID, info)
			}
		}
	}()
}

func (d *DiagnosticClient) Peers() map[string]PeerStatistics {
	return d.peersStats.GetPeers()
}
