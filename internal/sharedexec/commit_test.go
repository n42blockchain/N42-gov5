package sharedexec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func emptyChild(chain *internal.BlockChain, parent *block.Block) *block.Block {
	number := parent.Number64().Uint64() + 1
	result := internal.ExecutedResultFor(chain.Config(), number, parent.StateRoot(), nil)
	h := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(number), Time: parent.Time() + 1,
		GasLimit: parent.GasLimit(), Difficulty: uint256.NewInt(number + 1), BaseFee: uint256.NewInt(1),
		Root: result.Root, ReceiptHash: result.ReceiptHash, Bloom: result.Bloom}
	return block.NewBlockFromReceipt(h, nil, nil, nil, nil).(*block.Block)
}

func TestCanonicalCommitOverUnixWithRealExecution(t *testing.T) {
	ctx := context.Background()
	chain, first, _ := executionFixture(t, true)
	var owner [32]byte
	if _, err := rand.Read(owner[:]); err != nil {
		t.Fatal(err)
	}
	server, err := NewWithTrustedCommitOwner(chain, 8, owner)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	for _, payload := range []string{`{"sender":"trusted"}`, `{}` + `{}`, strings.Repeat(" ", 4097)} {
		r := httptest.NewRequest(http.MethodPost, "/v1/commit", strings.NewReader(payload))
		r.Header.Set("X-N42-Commit-Owner", hex.EncodeToString(owner[:]))
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid commit framing accepted: %d", w.Code)
		}
	}
	request := func(b *block.Block) CommitRequest {
		return CommitRequest{Identity: server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64()}
	}
	importBlock := func(b *block.Block) {
		raw, err := b.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
		if err != nil || !result.Durable {
			t.Fatalf("real import: %+v %v", result, err)
		}
	}
	assertHead := func(b block.IBlock) {
		t.Helper()
		if err := chain.DB().View(ctx, func(tx kv.Tx) error {
			head := rawdb.ReadHeadBlockHash(tx)
			canonical, err := rawdb.ReadCanonicalHash(tx, b.Number64().Uint64())
			if err != nil {
				return err
			}
			if head != b.Hash() || canonical != b.Hash() {
				t.Fatalf("head changed incorrectly: %s %s", head, canonical)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Commit(ctx, request(first), owner); err == nil {
		t.Fatal("missing block committed")
	}
	importBlock(first)
	assertHead(chain.GenesisBlock()) // Import is side-only, not finality.
	wrongOwner := owner
	wrongOwner[0] ^= 1
	if _, err := client.Commit(ctx, request(first), wrongOwner); err == nil {
		t.Fatal("unauthorized commit accepted")
	}
	bad := request(first)
	bad.Identity.GenesisHash[0] ^= 1
	if _, err := client.Commit(ctx, bad, owner); err == nil {
		t.Fatal("wrong genesis committed")
	}
	bad = request(first)
	bad.Number++
	if _, err := client.Commit(ctx, bad, owner); err == nil {
		t.Fatal("wrong height committed")
	}
	bad = request(first)
	bad.ParentHash[0] ^= 1
	if _, err := client.Commit(ctx, bad, owner); err == nil {
		t.Fatal("wrong parent committed")
	}
	second := emptyChild(chain, first)
	importBlock(second)
	if _, err := client.Commit(ctx, request(second), owner); err == nil {
		t.Fatal("skipped canonical parent accepted")
	}
	assertHead(chain.GenesisBlock())
	for _, b := range []*block.Block{first, first, second, first, second} {
		result, err := client.Commit(ctx, request(b), owner)
		if err != nil || !result.Committed {
			t.Fatalf("commit %d: %+v %v", b.Number64().Uint64(), result, err)
		}
	}
	assertHead(second)
	// A stored-only child has headers/body but no executed state or receipts.
	third := emptyChild(chain, second)
	forkHeader := first.Header().(*block.Header)
	forkHeader.Time++
	fork := first.WithSeal(forkHeader)
	if err := chain.DB().Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, third); err != nil {
			return err
		}
		return rawdb.WriteBlock(tx, fork)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Commit(ctx, request(third), owner); err == nil {
		t.Fatal("stored-only block committed")
	}
	if _, err := client.Commit(ctx, request(fork), owner); err == nil {
		t.Fatal("finalized fork overwritten")
	}
	assertHead(second)
	if err := chain.DB().View(ctx, func(tx kv.Tx) error {
		if rawdb.ReadHotStuffCommittedHead(tx) != second.Hash() {
			t.Fatal("finality marker not durable")
		}
		if !chain.HasAppliedBlock(second.Hash(), 2) {
			t.Fatal("applied lineage lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if server.ImportCalls() != 2 {
		t.Fatalf("unexpected execution count %d", server.ImportCalls())
	}
}

func TestCommitDisabledByDefault(t *testing.T) {
	chain, b, _ := executionFixture(t, true)
	server, err := New(chain, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var owner [32]byte
	owner[0] = 1
	if _, err := server.Commit(context.Background(), CommitRequest{Identity: server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: 1}, owner); err == nil {
		t.Fatal("default server accepted commit")
	}
	if _, err := NewWithTrustedCommitOwner(chain, 1, [32]byte{}); err == nil {
		t.Fatal("empty owner accepted")
	}
}
