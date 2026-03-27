package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/blst"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

func generateTestVotes(n int) ([]pendingVote, []common.SecretKey) {
	votes := make([]pendingVote, n)
	keys := make([]common.SecretKey, n)
	msg := SigningMessage(42, [32]byte{0x01})

	for i := 0; i < n; i++ {
		sk, _ := bls.RandKey()
		keys[i] = sk
		sig := sk.Sign(msg)
		votes[i] = pendingVote{
			voter:    ValidatorIndex(i),
			sigBytes: sig.Marshal(),
			pk:       sk.PublicKey(),
			message:  msg,
		}
	}
	return votes, keys
}

func TestBatchVerifyVotes_AllValid(t *testing.T) {
	votes, _ := generateTestVotes(20)
	valid := batchVerifyVotes(votes)
	if len(valid) != 20 {
		t.Fatalf("expected 20 valid votes, got %d", len(valid))
	}
}

func TestBatchVerifyVotes_SingleVote(t *testing.T) {
	votes, _ := generateTestVotes(1)
	valid := batchVerifyVotes(votes)
	if len(valid) != 1 {
		t.Fatalf("expected 1 valid vote, got %d", len(valid))
	}
}

func TestBatchVerifyVotes_EmptySlice(t *testing.T) {
	valid := batchVerifyVotes(nil)
	if len(valid) != 0 {
		t.Fatalf("expected 0 valid votes, got %d", len(valid))
	}
}

func TestBatchVerifyVotes_WithInvalidSig(t *testing.T) {
	votes, _ := generateTestVotes(10)
	// Corrupt signature of vote 5.
	votes[5].sigBytes = make([]byte, 96)
	valid := batchVerifyVotes(votes)
	if len(valid) != 9 {
		t.Fatalf("expected 9 valid votes (1 invalid), got %d", len(valid))
	}
	for _, v := range valid {
		if v.voter == 5 {
			t.Fatal("invalid vote (voter 5) should have been filtered out")
		}
	}
}

func TestBatchVerifyVotes_WrongPubkey(t *testing.T) {
	votes, _ := generateTestVotes(5)
	// Replace pubkey of vote 2 with a different key.
	wrongSK, _ := bls.RandKey()
	votes[2].pk = wrongSK.PublicKey()
	valid := batchVerifyVotes(votes)
	if len(valid) != 4 {
		t.Fatalf("expected 4 valid votes (1 wrong pk), got %d", len(valid))
	}
}

func TestBatchVerifyVotes_MixedMessageLengths(t *testing.T) {
	// Round 1 messages (40 bytes) and Round 2 messages (46 bytes).
	msg1 := SigningMessage(1, [32]byte{0xAA})
	msg2 := CommitSigningMessage(1, [32]byte{0xAA})

	sk1, _ := bls.RandKey()
	sk2, _ := bls.RandKey()

	votes := []pendingVote{
		{voter: 0, sigBytes: sk1.Sign(msg1).Marshal(), pk: sk1.PublicKey(), message: msg1},
		{voter: 1, sigBytes: sk2.Sign(msg2).Marshal(), pk: sk2.PublicKey(), message: msg2},
	}

	valid := batchVerifyVotes(votes)
	if len(valid) != 2 {
		t.Fatalf("expected 2 valid votes with mixed message lengths, got %d", len(valid))
	}
}

func TestVerifyMultipleSignaturesVarMsg(t *testing.T) {
	n := 50
	sigs := make([][]byte, n)
	msgs := make([][]byte, n)
	pks := make([]common.PublicKey, n)

	for i := 0; i < n; i++ {
		sk, _ := bls.RandKey()
		pks[i] = sk.PublicKey()
		// Use variable-length messages.
		if i%2 == 0 {
			msgs[i] = SigningMessage(ViewNumber(i), [32]byte{byte(i)})
		} else {
			msgs[i] = CommitSigningMessage(ViewNumber(i), [32]byte{byte(i)})
		}
		sig := sk.Sign(msgs[i])
		sigs[i] = sig.Marshal()
	}

	ok, err := blst.VerifyMultipleSignaturesVarMsg(sigs, msgs, pks)
	if err != nil {
		t.Fatalf("VerifyMultipleSignaturesVarMsg error: %v", err)
	}
	if !ok {
		t.Fatal("expected batch verification to pass")
	}
}

// Benchmarks

func BenchmarkIndividualBLSVerify(b *testing.B) {
	votes, _ := generateTestVotes(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, v := range votes {
			VerifyBLSSignature(v.sigBytes, v.pk, v.message)
		}
	}
}

func BenchmarkBatchBLSVerify(b *testing.B) {
	votes, _ := generateTestVotes(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batchVerifyVotes(votes)
	}
}
