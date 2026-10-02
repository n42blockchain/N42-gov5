// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"errors"
	"testing"
)

// TestDispatchMessage_InvalidPayloads covers every "wrong/nil payload for
// this message type" guard in dispatchMessage's switch, plus the nil-payload
// and unknown-type fallthroughs.
func TestDispatchMessage_InvalidPayloads(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)

	cases := []struct {
		name string
		msg  ConsensusMsg
	}{
		{"nil payload", ConsensusMsg{Type: MsgVote, Payload: nil}},
		{"unknown type", ConsensusMsg{Type: ConsensusMsgType(99), Payload: &Vote{}}},
		{"proposal wrong type", ConsensusMsg{Type: MsgProposal, Payload: &Vote{}}},
		{"vote wrong type", ConsensusMsg{Type: MsgVote, Payload: &Proposal{}}},
		{"commitVote wrong type", ConsensusMsg{Type: MsgCommitVote, Payload: &Vote{}}},
		{"prepareQC wrong type", ConsensusMsg{Type: MsgPrepareQC, Payload: &Vote{}}},
		{"timeout wrong type", ConsensusMsg{Type: MsgTimeout, Payload: &Vote{}}},
		{"newView wrong type", ConsensusMsg{Type: MsgNewView, Payload: &Vote{}}},
		{"decide wrong type", ConsensusMsg{Type: MsgDecide, Payload: &Vote{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := e.dispatchMessage(tc.msg, msgTiming{}); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("dispatchMessage(%s) = %v, want ErrInvalidMessage", tc.name, err)
			}
		})
	}
}

// TestMessageView_NilAndUnknown covers messageView's nil-payload and
// unknown-type zero-value fallthroughs (the typed cases are already
// exercised via extractQCFromMessage's sibling test and normal dispatch).
func TestMessageView_NilAndUnknown(t *testing.T) {
	if v := messageView(ConsensusMsg{Type: MsgVote, Payload: nil}); v != 0 {
		t.Fatalf("messageView(nil payload) = %d, want 0", v)
	}
	if v := messageView(ConsensusMsg{Type: ConsensusMsgType(99), Payload: &Vote{View: 5}}); v != 0 {
		t.Fatalf("messageView(unknown type) = %d, want 0", v)
	}
	if v := messageView(ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 7}}); v != 7 {
		t.Fatalf("messageView(vote) = %d, want 7", v)
	}
}
