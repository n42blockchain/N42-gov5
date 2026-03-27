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

package download

import (
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/proto/sync_proto"
	"github.com/n42blockchain/N42/common/message"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/common/utils"
	"google.golang.org/protobuf/proto"
)

// fetchHeaders downloads block headers in batches from available peers.
func (d *Downloader) fetchHeaders(from uint256.Int, latest uint256.Int) error {
	begin := new(uint256.Int).AddUint64(&from, 1)
	difference := new(uint256.Int).Sub(&latest, begin)
	tasks := new(uint256.Int).Add(new(uint256.Int).Div(difference, uint256.NewInt(maxHeaderFetch)), uint256.NewInt(1))
	log.Infof("Starting header downloads from: %v latest: %v difference: %v task: %v", begin.Uint64(), latest.Uint64(), difference.Uint64(), tasks.Uint64())
	defer log.Infof("Header download Finished")

	d.headerTaskLock.Lock()
	for i := 1; i <= int(tasks.Uint64()); i++ {
		taskID := misc.SecureUint64()
		d.headerTasks = append(d.headerTasks, Task{
			taskID:     taskID,
			IndexBegin: *begin,
			IsSync:     false,
		})
		begin = begin.Add(begin, uint256.NewInt(maxHeaderFetch))
	}
	d.headerTaskLock.Unlock()

	tick := time.NewTicker(syncPeerIntervalRequest)
	defer tick.Stop()

	for {
		log.Tracef("header tasks count is %v, header processing tasks count is: %v", len(d.headerTasks), len(d.headerProcessingTasks))

		if len(d.headerTasks) == 0 && len(d.headerProcessingTasks) == 0 { // break if there are no tasks
			break
		}

		peerSet := d.peersInfo.findPeers(&latest, syncPeerCount)
		d.headerTaskLock.Lock()
		if len(d.headerTasks) > 0 {
			for _, p := range peerSet {
				task := d.headerTasks[0]

				var fetchCount uint64
				latestVal := latest.Uint64()
				beginVal := task.IndexBegin.Uint64()
				if latestVal >= beginVal && latestVal-beginVal >= maxHeaderFetch-1 {
					fetchCount = maxHeaderFetch
				} else if latestVal >= beginVal {
					fetchCount = latestVal - beginVal + 1
				} else {
					continue
				}

				msg := &sync_proto.SyncTask{
					Id:       task.taskID,
					SyncType: sync_proto.SyncType_HeaderReq,
					Payload: &sync_proto.SyncTask_SyncHeaderRequest{
						SyncHeaderRequest: &sync_proto.SyncHeaderRequest{
							Number: utils.ConvertUint256IntToH256(&task.IndexBegin),
							Amount: utils.ConvertUint256IntToH256(uint256.NewInt(fetchCount)),
						},
					},
				}
				payload, err := proto.Marshal(msg)
				if err != nil {
					log.Errorf("failed to marshal header request for peer %v: %v", p.ID(), err)
					continue
				}
				if err = p.WriteMsg(message.MsgDownloader, payload); err != nil {
					log.Errorf("send sync request message to peer %v err is %v", p.ID(), err)
					continue
				}
				task.TimeBegin = time.Now()
				d.headerTasks = d.headerTasks[1:]
				d.headerProcessingTasks[task.taskID] = task
				if len(d.headerTasks) == 0 {
					break
				}
			}
		}

		// If it times out, put the task back
		if len(d.headerProcessingTasks) > 0 {
			for taskID, task := range d.headerProcessingTasks {
				if time.Since(task.TimeBegin) > syncTimeOutPerRequest {
					delete(d.headerProcessingTasks, taskID)
					task.TimeBegin = time.Now()
					d.headerTasks = append(d.headerTasks, task)
				}
			}
		}
		d.headerTaskLock.Unlock()

		select {
		case <-d.ctx.Done():
			return ErrCanceled
		case <-tick.C:
			tick.Reset(syncPeerIntervalRequest)
			continue
		}
	}

	return nil
}

// fetchBodies downloads block bodies in batches from available peers.
func (d *Downloader) fetchBodies(latest uint256.Int) error {
	defer log.Info("Bodies download Finished")

	tick := time.NewTicker(syncPeerIntervalRequest)
	defer tick.Stop()
	startProcess := false

	for {
		// If there are unprocessed tasks
		peerSet := d.peersInfo.findPeers(&latest, syncPeerCount)

		log.Tracef("downloader body task count is %d, processing task count is %d", len(d.bodyTaskPool), len(d.bodyProcessingTasks))
		if startProcess && len(d.bodyTaskPool) == 0 && len(d.bodyProcessingTasks) == 0 {
			return nil
		}

		d.once.Do(func() {
			log.Info("Starting body downloads")
			startProcess = true
		})

		d.bodyTaskPoolLock.Lock()
		if len(d.bodyTaskPool) > 0 {
			for _, p := range peerSet {
				task := d.bodyTaskPool[0]
				msg := &sync_proto.SyncTask{
					Id:       task.taskID,
					SyncType: sync_proto.SyncType_BodyReq,
					Payload: &sync_proto.SyncTask_SyncBlockRequest{
						SyncBlockRequest: &sync_proto.SyncBlockRequest{
							Number: utils.Uint256sToH256(task.number),
						},
					},
				}
				payload, err := proto.Marshal(msg)
				if err != nil {
					log.Errorf("failed to marshal body request for peer %v: %v", p.ID(), err)
					continue
				}
				if err = p.WriteMsg(message.MsgDownloader, payload); err != nil {
					log.Errorf("send sync request message to peer %v err is %v", p.ID(), err)
					continue
				}
				d.bodyTaskPool = d.bodyTaskPool[1:]
				d.bodyProcessingTasks[task.taskID] = task
				if len(d.bodyTaskPool) == 0 {
					break
				}
			}
		}
		d.bodyTaskPoolLock.Unlock()

		select {
		case <-d.ctx.Done():
			return ErrCanceled

		case <-tick.C:
			tick.Reset(syncPeerIntervalRequest)
			continue
		}
	}
}
