package sync

import "testing"

const (
	validTopicA = "/n42/aabbccdd/block"
	validTopicB = "/n42/aabbccdd/transaction"
	badTopic    = "not-a-valid-topic"
)

func TestSubTopicHandlerAddExistsRemove(t *testing.T) {
	h := newSubTopicHandler()

	if h.topicExists(validTopicA) {
		t.Fatalf("expected topic not to exist before add")
	}

	h.addTopic(validTopicA, nil)
	if !h.topicExists(validTopicA) {
		t.Fatalf("expected topic to exist after add")
	}

	digest := [4]byte{0xaa, 0xbb, 0xcc, 0xdd}
	if !h.digestExists(digest) {
		t.Fatalf("expected digest to exist after add")
	}

	// A second topic sharing the same digest keeps the digest alive after
	// the first is removed.
	h.addTopic(validTopicB, nil)
	h.removeTopic(validTopicA)
	if h.topicExists(validTopicA) {
		t.Fatalf("expected topic removed")
	}
	if !h.digestExists(digest) {
		t.Fatalf("expected digest to remain while second topic still registered")
	}

	h.removeTopic(validTopicB)
	if h.digestExists(digest) {
		t.Fatalf("expected digest removed once all topics for it are gone")
	}
}

func TestSubTopicHandlerAddRemoveInvalidTopic(t *testing.T) {
	h := newSubTopicHandler()
	// Invalid topics should not panic, and simply skip digest bookkeeping.
	h.addTopic(badTopic, nil)
	if !h.topicExists(badTopic) {
		t.Fatalf("expected topic map entry despite invalid digest")
	}
	h.removeTopic(badTopic)
	if h.topicExists(badTopic) {
		t.Fatalf("expected topic removed")
	}
}

func TestSubTopicHandlerRemoveUnknownDigest(t *testing.T) {
	h := newSubTopicHandler()
	// Removing a topic never added should not panic or underflow.
	h.removeTopic(validTopicA)
	if h.digestExists([4]byte{0xaa, 0xbb, 0xcc, 0xdd}) {
		t.Fatalf("expected no digest entry")
	}
}

func TestSubTopicHandlerAllTopicsAndSubForTopic(t *testing.T) {
	h := newSubTopicHandler()
	h.addTopic(validTopicA, nil)
	h.addTopic(validTopicB, nil)

	topics := h.allTopics()
	if len(topics) != 2 {
		t.Fatalf("allTopics() returned %d topics, want 2", len(topics))
	}

	if sub := h.subForTopic(validTopicA); sub != nil {
		t.Fatalf("expected nil subscription for unregistered sub value")
	}
	if sub := h.subForTopic("/n42/unregistered/topic"); sub != nil {
		t.Fatalf("expected nil subscription for unknown topic")
	}
}
