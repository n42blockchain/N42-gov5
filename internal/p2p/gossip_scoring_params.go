package p2p

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"
)

const (
	beaconBlockWeight   = 0.8
	voluntaryExitWeight = 0.05
	maxInMeshScore      = 10
	decayToZero         = 0.01
)

var (
	oneHundredBlocks   = 100 * oneBlockDuration()
	invalidDecayPeriod = 50 * oneBlockDuration()
	twentyBlocks       = 20 * oneBlockDuration()
	tenBlocks          = 10 * oneBlockDuration()
)

func peerScoringParams() (*pubsub.PeerScoreParams, *pubsub.PeerScoreThresholds) {
	thresholds := &pubsub.PeerScoreThresholds{
		GossipThreshold:             -4000,
		PublishThreshold:            -8000,
		GraylistThreshold:           -16000,
		AcceptPXThreshold:           100,
		OpportunisticGraftThreshold: 5,
	}
	scoreParams := &pubsub.PeerScoreParams{
		Topics:                      make(map[string]*pubsub.TopicScoreParams),
		TopicScoreCap:               32.72,
		AppSpecificScore:            func(_ peer.ID) float64 { return 0 },
		AppSpecificWeight:           1,
		IPColocationFactorWeight:    -35.11,
		IPColocationFactorThreshold: 10,
		BehaviourPenaltyWeight:      -15.92,
		BehaviourPenaltyThreshold:   6,
		BehaviourPenaltyDecay:       scoreDecay(tenBlocks),
		DecayInterval:               oneBlockDuration(),
		DecayToZero:                 decayToZero,
		RetainScore:                 oneHundredBlocks,
	}
	return scoreParams, thresholds
}

func (s *Service) topicScoreParams(topic string) (*pubsub.TopicScoreParams, error) {
	switch {
	case strings.Contains(topic, GossipBlockMessage):
		return blockTopicParams(), nil
	case strings.Contains(topic, GossipExitMessage):
		return voluntaryExitTopicParams(), nil
	case strings.Contains(topic, GossipBlobSidecarMessage):
		return blockTopicParams(), nil
	default:
		return nil, errors.Errorf("unrecognized topic for parameter registration: %s", topic)
	}
}

// blockTopicParams returns scoring parameters for the block gossip topic.
// Based on lighthouse parameters:
// https://gist.github.com/blacktemplar/5c1862cb3f0e32a1a7fb0b25e79e6e2c
func blockTopicParams() *pubsub.TopicScoreParams {
	const decayEpoch = time.Duration(5)
	blockDur := oneBlockDuration()
	mesh := inMeshCap()

	return &pubsub.TopicScoreParams{
		TopicWeight:                     beaconBlockWeight,
		TimeInMeshWeight:                maxInMeshScore / mesh,
		TimeInMeshQuantum:               blockDur,
		TimeInMeshCap:                   mesh,
		FirstMessageDeliveriesWeight:    1,
		FirstMessageDeliveriesDecay:     scoreDecay(twentyBlocks),
		FirstMessageDeliveriesCap:       23,
		MeshMessageDeliveriesWeight:     0,
		MeshMessageDeliveriesDecay:      scoreDecay(decayEpoch * blockDur),
		MeshMessageDeliveriesCap:        float64(decayEpoch),
		MeshMessageDeliveriesThreshold:  float64(decayEpoch) / 10,
		MeshMessageDeliveriesWindow:     2 * time.Second,
		MeshMessageDeliveriesActivation: 4 * blockDur,
		MeshFailurePenaltyWeight:        0,
		MeshFailurePenaltyDecay:         scoreDecay(decayEpoch * blockDur),
		InvalidMessageDeliveriesWeight:  -140.4475,
		InvalidMessageDeliveriesDecay:   scoreDecay(invalidDecayPeriod),
	}
}

// voluntaryExitTopicParams returns scoring parameters for the voluntary exit gossip topic.
func voluntaryExitTopicParams() *pubsub.TopicScoreParams {
	mesh := inMeshCap()

	return &pubsub.TopicScoreParams{
		TopicWeight:                    voluntaryExitWeight,
		TimeInMeshWeight:               maxInMeshScore / mesh,
		TimeInMeshQuantum:              oneBlockDuration(),
		TimeInMeshCap:                  mesh,
		FirstMessageDeliveriesWeight:   2,
		FirstMessageDeliveriesDecay:    scoreDecay(oneHundredBlocks),
		FirstMessageDeliveriesCap:      5,
		InvalidMessageDeliveriesWeight: -2000,
		InvalidMessageDeliveriesDecay:  scoreDecay(invalidDecayPeriod),
	}
}

func oneBlockDuration() time.Duration {
	return 8 * time.Second
}

// scoreDecay computes the per-block decay rate such that an initial value of 1
// reaches decayToZero over the given total duration.
func scoreDecay(totalDuration time.Duration) float64 {
	numBlocks := totalDuration / oneBlockDuration()
	return math.Pow(decayToZero, 1/float64(numBlocks))
}

func inMeshCap() float64 {
	return float64((3600 * time.Second) / oneBlockDuration())
}

// logGossipParameters logs all fields of a TopicScoreParams struct for debugging.
func logGossipParameters(topic string, params *pubsub.TopicScoreParams) {
	if params == nil {
		return
	}
	v := reflect.ValueOf(params).Elem()
	t := v.Type()
	fields := make([]interface{}, 0, t.NumField()*2)
	for i := 0; i < t.NumField(); i++ {
		fields = append(fields, t.Field(i).Name, v.Field(i).Interface())
	}
	log.Debug(fmt.Sprintf("Topic Parameters for %s", topic), fields...)
}
