// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// Vector V9 of the mixed production standard (Profile 1): gov5's GossipSub
// parameters as a wire contract (G2, G3, G4, B15): topic strings on the V1
// genesis, the message id, the router's mesh and gossip parameters, the
// message-size cap after Profile 1's SetMaxBlockBytes, the router options and
// the peer-scoring parameters. Everything is read from gov5's p2p code.
//
// N42_PROFILE1_VECTORS_UPDATE=1 rewrites testdata/profile1/vectors/V9;
// N42_PROFILE1_VECTORS_MIRROR=<dir> also writes the files to <dir>/V9.

const (
	v9Dir         = "../testdata/profile1/vectors"
	v9FileName    = "gossipsub_parameters.json"
	v9MaxBlock    = uint64(64 << 20) // standard genesis maxBlockBytes (B15)
	v9UpdateEnv   = "N42_PROFILE1_VECTORS_UPDATE"
	v9MirrorEnv   = "N42_PROFILE1_VECTORS_MIRROR"
	v9V1HeaderVec = "V1/header_profile_v1.json"
)

type v9Topic struct {
	Name     string `json:"name"`
	Topic    string `json:"topic"`
	Standard bool   `json:"inG2"`
	Scoring  string `json:"scoringParams"`
	Note     string `json:"note,omitempty"`
}

type v9MsgID struct {
	Topic string `json:"topic"`
	Data  string `json:"data"`
	ID    string `json:"messageId"`
}

type v9File struct {
	Vector         string                    `json:"vector"`
	Standard       string                    `json:"standard"`
	Library        string                    `json:"library"`
	GenesisHash    string                    `json:"genesisHash"`
	ForkDigest     string                    `json:"forkDigest"`
	Topics         []v9Topic                 `json:"topics"`
	MessageIDRule  string                    `json:"messageIdRule"`
	MessageIDs     []v9MsgID                 `json:"messageIds"`
	Router         map[string]any            `json:"router"`
	GossipSub      map[string]any            `json:"gossipSubParams"`
	ScoringEnabled string                    `json:"peerScoringEnabledWhen"`
	Thresholds     map[string]any            `json:"peerScoreThresholds"`
	PeerScore      map[string]any            `json:"peerScoreParams"`
	TopicScores    map[string]map[string]any `json:"topicScoreParams"`
	Notes          []string                  `json:"notes"`
}

// v9Fields flattens the exported scalar fields of a parameter struct;
// durations are given in milliseconds. Functions, maps and slices are left
// out (named in skipped).
func v9Fields(v any) (map[string]any, []string) {
	out := map[string]any{}
	var skipped []string
	rv := reflect.Indirect(reflect.ValueOf(v))
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := rv.Field(i)
		switch {
		case f.Type == reflect.TypeOf(time.Duration(0)):
			out[f.Name] = float64(fv.Int()) / float64(time.Millisecond)
		case f.Type.Kind() == reflect.Func || f.Type.Kind() == reflect.Map || f.Type.Kind() == reflect.Slice:
			skipped = append(skipped, f.Name)
		default:
			out[f.Name] = fv.Interface()
		}
	}
	return out, skipped
}

func v9PubsubVersion(t *testing.T) string {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "github.com/libp2p/go-libp2p-pubsub" {
			return f[0] + " " + f[1]
		}
	}
	t.Fatal("go-libp2p-pubsub not in go.mod")
	return ""
}

// TestProfile1VectorV9 generates and checks the V9 GossipSub vector.
func TestProfile1VectorV9(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(v9Dir, v9V1HeaderVec))
	if err != nil {
		t.Fatal(err)
	}
	var v1 struct {
		GenesisHash types.Hash `json:"genesisHash"`
	}
	if err := json.Unmarshal(raw, &v1); err != nil {
		t.Fatal(err)
	}
	genesis := v1.GenesisHash
	digest, err := utils.CreateForkDigest(nil, genesis)
	if err != nil {
		t.Fatal(err)
	}
	if digest != utils.ToBytes4(genesis[:]) {
		t.Fatal("fork digest is not the genesis hash prefix")
	}
	suffix := encoder.SszNetworkEncoder{}.ProtocolSuffix()

	// Profile 1 start-up: maxBlockBytes is the only block-size bound (B15).
	savedGossip, savedChunk := encoder.MaxGossipSize, encoder.MaxBlockChunkSize
	encoder.SetMaxBlockBytes(v9MaxBlock)
	defer func() { encoder.MaxGossipSize, encoder.MaxBlockChunkSize = savedGossip, savedChunk }()

	s := &Service{}
	out := v9File{
		Vector:      "V9",
		Standard:    "N42 mixed production standard profile 1; vector V9 (GossipSub parameters: G2, G3, G4, B15)",
		Library:     v9PubsubVersion(t),
		GenesisHash: genesis.Hex(),
		ForkDigest:  hex.EncodeToString(digest[:]),
		MessageIDRule: "message id = keccak256(genesisHash || topic || data)[:20] (G3); data = the message payload as published " +
			"(snappy block of the encoded message)",
		ScoringEnabled: "only when discovery is on (Service.peerScoringEnabled = !NoDiscovery); a node started with an explicit peer list and discovery off runs without peer scoring",
		TopicScores:    map[string]map[string]any{},
		Notes: []string{
			"Generated by gov5 TestProfile1VectorV9 (internal/p2p/profile1_v9_vectors_test.go) from pubsubGossipParam, pubsubOptions' constants, GossipMaxSize after encoder.SetMaxBlockBytes(maxBlockBytes), peerScoringParams, topicScoreParams and MsgID.",
			"Durations are in milliseconds. gossipSubParams is the full GossipSubParams struct gov5 hands to the router (libp2p defaults where gov5 sets nothing).",
			"Timeouts, mesh degree and scoring are local router behaviour, not consensus; a client that differs still interoperates but may prune or be pruned differently. Topic strings, message id and the size cap must match.",
		},
	}

	g2 := map[string]bool{GossipHotStuffConsensusMessage: true, GossipBlockMessage: true, GossipTransactionMessage: true,
		GossipH2V4Message: true, GossipMobilePacketMessage: true, GossipMobileRegistrationMessage: true}
	notes := map[string]string{
		GossipBlobSidecarMessage:        "gov5 subscribes to it on every chain (internal/sync registerSubscribers); not in G2, Profile 1 has no blobs (B9)",
		GossipZKProofMessage:            "in the topic registry, but no gov5 service joins it; topicScoreParams has no branch for it",
		GossipMobileCohortIndexMessage:  "joined by the mobileverify cohort coordinator when it runs (not in G2)",
		GossipMobileCohortRevealMessage: "joined by the mobileverify cohort coordinator when it runs (not in G2)",
		GossipMobileCohortCertMessage:   "joined by the mobileverify cohort coordinator when it runs (not in G2)",
		GossipTransactionMessage:        "joined when the node has a transaction pool",
		GossipH2V4Message:               "no fork digest: the H2-v4 envelope carries chain id and genesis hash",
	}
	scoringName := func(topic string) string {
		p, err := s.topicScoreParams(topic)
		if err != nil {
			return "refused: " + err.Error()
		}
		for name, f := range map[string]func() *pubsub.TopicScoreParams{
			"block": blockTopicParams, "consensus": hotstuffConsensusTopicParams, "voluntaryExit": voluntaryExitTopicParams, "lightweight": messagingTopicParams,
		} {
			if reflect.DeepEqual(p, f()) {
				return name
			}
		}
		t.Fatalf("topic %s: scoring params match no named set", topic)
		return ""
	}
	for _, format := range gossipTopics {
		var name, topic string
		if format == H2V4Topic {
			name, topic = GossipH2V4Message, H2V4Topic+suffix
		} else {
			topic = fmt.Sprintf(format, digest) + suffix
			name = format[len(GossipProtocolAndDigest):]
		}
		if !IsGossipTopic(format) {
			t.Fatalf("%s is not registered", format)
		}
		out.Topics = append(out.Topics, v9Topic{Name: name, Topic: topic, Standard: g2[name], Scoring: scoringName(topic), Note: notes[name]})
	}
	if got := H2V4Topic + suffix; got != "/n42/h2/4/ssz_snappy" {
		t.Fatalf("h2/4 topic %q", got)
	}

	for _, c := range []struct{ topic, data string }{
		{fmt.Sprintf(HotStuffConsensusTopicFormat, digest) + suffix, ""},
		{fmt.Sprintf(HotStuffConsensusTopicFormat, digest) + suffix, "00"},
		{fmt.Sprintf(BlockTopicFormat, digest) + suffix, "ff00ff00"},
		{H2V4Topic + suffix, "4e3432483256340102"},
	} {
		data, _ := hex.DecodeString(c.data)
		topic := c.topic
		id := MsgID(genesis, &pubsubpb.Message{Topic: &topic, Data: data})
		out.MessageIDs = append(out.MessageIDs, v9MsgID{Topic: c.topic, Data: "0x" + c.data, ID: "0x" + hex.EncodeToString([]byte(id))})
	}

	out.Router = map[string]any{
		"messageSignaturePolicy":   "StrictNoSign",
		"noAuthor":                 true,
		"maxMessageSizeBytes":      GossipMaxSize(),
		"maxDecodedMessageBytes":   encoder.MaxGossipSize,
		"maxMessageSizeRule":       "snappy.MaxEncodedLen(maxBlockBytes) = 32 + n + n/6 for n = maxBlockBytes",
		"seenMessagesTTLMs":        float64(seenMessagesTTL) / float64(time.Millisecond),
		"timeCacheDurationMs":      float64(550*gossipSubHeartbeatInterval) / float64(time.Millisecond),
		"peerOutboundQueueSize":    pubsubQueueSize,
		"validateQueueSize":        pubsubQueueSize,
		"subscriptionFilterPrefix": gossipTopicPrefix,
		"protocolSuffix":           suffix,
	}
	if GossipMaxSize() != 32+v9MaxBlock+v9MaxBlock/6 {
		t.Fatalf("router cap %d", GossipMaxSize())
	}
	gp := pubsubGossipParam()
	var skipped []string
	out.GossipSub, skipped = v9Fields(gp)
	if len(skipped) != 0 || gp.D != 8 || gp.Dlo != 6 {
		t.Fatalf("gossip params: skipped %v, D %d Dlo %d", skipped, gp.D, gp.Dlo)
	}
	ps, th := peerScoringParams()
	out.Thresholds, _ = v9Fields(th)
	out.PeerScore, skipped = v9Fields(ps)
	out.PeerScore["AppSpecificScore"] = "always 0"
	out.PeerScore["skippedFields"] = skipped
	for name, f := range map[string]func() *pubsub.TopicScoreParams{
		"block": blockTopicParams, "consensus": hotstuffConsensusTopicParams, "voluntaryExit": voluntaryExitTopicParams, "lightweight": messagingTopicParams,
	} {
		out.TopicScores[name], _ = v9Fields(f())
	}

	enc, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	enc = append(enc, '\n')
	path := filepath.Join(v9Dir, "V9", v9FileName)
	if os.Getenv(v9UpdateEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, enc, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if dir := os.Getenv(v9MirrorEnv); dir != "" {
		if err := os.MkdirAll(filepath.Join(dir, "V9"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "V9", v9FileName), enc, 0o644); err != nil {
			t.Fatal(err)
		}
		readme, err := os.ReadFile(filepath.Join(v9Dir, "V9", "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "V9", "README.md"), readme, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (set %s=1 to generate)", path, err, v9UpdateEnv)
	}
	if !bytes.Equal(have, enc) {
		t.Fatalf("%s differs from the generated vector (set %s=1 to regenerate)", path, v9UpdateEnv)
	}
}
