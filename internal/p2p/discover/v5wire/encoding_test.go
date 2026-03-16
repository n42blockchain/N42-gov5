package v5wire

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/enode"
)

func TestMakeHeaderRejectsInvalidFlag(t *testing.T) {
	var c Codec
	_, err := c.makeHeader(enode.ID{}, 0xff, 0)
	if err == nil {
		t.Fatal("expected invalid flag error")
	}
	if !errors.Is(err, errInvalidHeaderFlag) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEncodeWhoareyouRejectsMissingNode(t *testing.T) {
	var c Codec
	_, err := c.encodeWhoareyou(enode.ID{}, &Whoareyou{RecordSeq: 1})
	if !errors.Is(err, errMissingNode) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEncodeHandshakeHeaderRejectsMissingChallengeNode(t *testing.T) {
	var c Codec
	_, _, err := c.encodeHandshakeHeader(enode.ID{}, "", &Whoareyou{})
	if !errors.Is(err, errMissingChallenge) {
		t.Fatalf("unexpected error: %v", err)
	}
}
