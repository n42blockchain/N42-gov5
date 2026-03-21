package sync

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/holiman/uint256"
	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/utils"
)

// maintainPeerStatuses periodically polls peers for their latest status.
func (s *Service) maintainPeerStatuses() {
	s.wg.Add(1)
	utils.RunEveryWithWG(s.ctx, maintainPeerStatusesInterval, func() {
		wg := new(sync.WaitGroup)
		for _, pid := range s.cfg.p2p.Peers().Connected() {
			wg.Add(1)
			go func(id peer.ID) {
				defer wg.Done()

				// If our peer status has not been updated correctly, disconnect and
				// update the connection state.
				if s.cfg.p2p.Host().Network().Connectedness(id) != network.Connected {
					s.cfg.p2p.Peers().SetConnectionState(id, peers.PeerDisconnecting)
					if err := s.cfg.p2p.Disconnect(id); err != nil {
						log.Debug("Error when disconnecting with peer", "err", err)
					}
					s.cfg.p2p.Peers().SetConnectionState(id, peers.PeerDisconnected)
					return
				}

				// Disconnect from peers that are considered bad by any registered scorer.
				if s.cfg.p2p.Peers().IsBad(id) {
					s.disconnectBadPeer(s.ctx, id)
					return
				}

				// Revalidate the peer if the status hasn't been updated recently.
				lastUpdated, err := s.cfg.p2p.Peers().ChainStateLastUpdated(id)
				if err != nil {
					return
				}
				if time.Now().After(lastUpdated.Add(maintainPeerStatusesInterval)) {
					if err := s.reValidatePeer(s.ctx, id); err != nil {
						log.Debug("Could not revalidate peer", "peer", id, "err", err)
						s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(id)
					}
				}
			}(pid)
		}

		// Wait for all status checks to finish, then prune excess peers.
		wg.Wait()
		for _, id := range s.cfg.p2p.Peers().PeersToPrune() {
			if err := s.sendGoodByeAndDisconnect(s.ctx, p2ptypes.GoodbyeCodeTooManyPeers, id); err != nil {
				log.Debug("Could not disconnect with peer", "peer", id, "err", err)
			}
		}
	}, &s.wg)
}

// resyncIfBehind checks periodically to see if the node has fallen behind its peers
// by more than a threshold, in which case it attempts a resync using initial sync.
func (s *Service) resyncIfBehind() {
	s.wg.Add(1)
	utils.RunEveryWithWG(s.ctx, resyncInterval, func() {
		if s.cfg.initialSync != nil && !s.cfg.initialSync.Syncing() {
			currentHeight := currentBlockNumber(s.cfg.chain)
			highestBlockNr, _ := s.cfg.p2p.Peers().BestPeers(s.cfg.p2p.GetConfig().MinSyncPeers*2, currentHeight)
			if highestBlockNr.Cmp(new(uint256.Int).AddUint64(currentHeight, 5)) >= 0 {
				log.Info("Fallen behind peers; reverting to initial sync to catch up",
					"currentBlockNr", currentHeight,
					"peersBlockNr", highestBlockNr,
				)
				numberOfTimesResyncedCounter.Inc()
				if err := s.cfg.initialSync.Resync(); err != nil {
					log.Error("Could not resync chain", "err", err)
				}
			}
		}
	}, &s.wg)
}

// sendRPCStatusRequest sends a Status RPC to the given peer and validates the response.
func (s *Service) sendRPCStatusRequest(ctx context.Context, id peer.ID) error {
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()

	resp := &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(s.genesisHashForStatus()),
		CurrentHeight: utils.ConvertUint256IntToH256(currentBlockNumber(s.cfg.chain)),
	}
	topic, err := p2p.TopicFromMessage(p2p.StatusMessageName)
	if err != nil {
		return err
	}
	stream, err := s.cfg.p2p.Send(ctx, resp, topic, id)
	if err != nil {
		return err
	}
	defer closeStream(stream)

	code, errMsg, err := ReadStatusCode(stream, s.cfg.p2p.Encoding())
	if err != nil {
		s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
		return err
	}
	if code != 0 {
		s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(id)
		return errors.New(errMsg)
	}

	msg := &sync_pb.Status{}
	if err := s.cfg.p2p.Encoding().DecodeWithMaxLength(stream, msg); err != nil {
		s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
		return err
	}

	err = s.validateStatusMessage(ctx, msg)
	s.cfg.p2p.Peers().Scorers().PeerStatusScorer().SetPeerStatus(id, msg, err)
	if s.cfg.p2p.Peers().IsBad(id) {
		s.disconnectBadPeer(s.ctx, id)
	}
	return err
}

func (s *Service) reValidatePeer(ctx context.Context, id peer.ID) error {
	s.cfg.p2p.Peers().Scorers().PeerStatusScorer().SetCurrentHeight(currentBlockNumber(s.cfg.chain))
	if err := s.sendRPCStatusRequest(ctx, id); err != nil {
		return err
	}
	// Do not return an error for ping requests.
	if err := s.sendPingRequest(ctx, id); err != nil {
		log.Debug("Could not ping peer", "err", err)
	}
	return nil
}

// statusRPCHandler reads the incoming Status RPC from the peer and responds with our status.
// This handler will disconnect any peer that does not match our fork version.
func (s *Service) statusRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, cancel := context.WithTimeout(ctx, ttfbTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	m, ok := msg.(*sync_pb.Status)
	if !ok {
		return errors.New("message is not type *pb.Status")
	}
	if err := s.rateLimiter.validateRequest(stream, 1); err != nil {
		return err
	}
	s.rateLimiter.add(stream, 1)

	remotePeer := stream.Conn().RemotePeer()
	if err := s.validateStatusMessage(ctx, m); err != nil {
		log.Debug("Invalid status message from peer", "handler", "status", "peer", remotePeer, "error", err)

		respCode := byte(0)
		switch err {
		case p2ptypes.ErrGeneric:
			respCode = responseCodeServerError
		case p2ptypes.ErrWrongForkDigestVersion:
			// Respond with our status and disconnect with the peer.
			s.cfg.p2p.Peers().SetChainState(remotePeer, m)
			if err := s.respondWithStatus(ctx, stream); err != nil {
				return err
			}
			closeStreamAndWait(stream)
			if err := s.sendGoodByeAndDisconnect(ctx, p2ptypes.GoodbyeCodeWrongNetwork, remotePeer); err != nil {
				return err
			}
			return nil
		default:
			respCode = responseCodeInvalidRequest
			s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(remotePeer)
		}

		originalErr := err
		resp, err := s.generateErrorResponse(respCode, err.Error())
		if err != nil {
			log.Debug("Could not generate a response error", "err", err)
		} else if _, err := stream.Write(resp); err != nil {
			log.Debug("Could not write to stream", "err", err)
		}
		closeStreamAndWait(stream)
		if err := s.sendGoodByeAndDisconnect(ctx, p2ptypes.GoodbyeCodeGenericError, remotePeer); err != nil {
			return err
		}
		return originalErr
	}

	s.cfg.p2p.Peers().SetChainState(remotePeer, m)
	if err := s.respondWithStatus(ctx, stream); err != nil {
		return err
	}
	closeStream(stream)
	return nil
}

func (s *Service) respondWithStatus(_ context.Context, stream network.Stream) error {
	resp := &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(s.genesisHashForStatus()),
		CurrentHeight: utils.ConvertUint256IntToH256(currentBlockNumber(s.cfg.chain)),
	}
	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		log.Debug("Could not write to stream", "err", err)
	}
	_, err := s.cfg.p2p.Encoding().EncodeWithMaxLength(stream, resp)
	return err
}

func (s *Service) validateStatusMessage(ctx context.Context, msg *sync_pb.Status) error {
	forkDigest, err := s.currentForkDigest()
	if err != nil {
		return err
	}
	remoteDigest, err := utils.CreateForkDigest(
		utils.ConvertH256ToUint256Int(msg.CurrentHeight),
		utils.ConvertH256ToHash(msg.GenesisHash),
	)
	if err != nil {
		return err
	}
	if !bytes.Equal(forkDigest[:], remoteDigest[:]) {
		return p2ptypes.ErrWrongForkDigestVersion
	}
	return nil
}
