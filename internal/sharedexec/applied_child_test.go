package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestAppliedChildRejectsSiblingImportWithoutQueueingOrReexecution(t *testing.T) {
	ctx := context.Background()
	f := NewExecutionFixtureForTest(t)
	s, err := New(f.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(startExecutionSocket(t, s), s.Identity())
	defer c.Close()
	build := func(template *block.Block) *block.Block {
		result, err := c.Build(ctx, buildRequest(t, s.Identity(), template))
		if err != nil {
			t.Fatal(err)
		}
		var candidate block.Block
		if err := candidate.Unmarshal(result.BlockRLP); err != nil {
			t.Fatal(err)
		}
		sealed := make(chan block.IBlock, 1)
		if err := f.Leader.Seal(f.Chain, &candidate, sealed, make(chan struct{})); err != nil {
			t.Fatal(err)
		}
		return (<-sealed).(*block.Block)
	}
	a := build(f.Template)
	header := block.CopyHeader(f.Template.Header().(*block.Header))
	header.GasLimit++
	b := build(block.NewBlock(header, f.Template.Transactions()).(*block.Block))
	for i, candidate := range []*block.Block{a, b} {
		raw, err := candidate.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		result, err := c.Import(ctx, ImportRequest{Identity: s.Identity(), BlockHash: candidate.Hash(), BlockRLP: raw})
		if err != nil || result.Durable != (i == 0) || (i == 1 && (result.State != "accepted" || result.Checked || result.Executed)) {
			t.Fatalf("import %d: %+v %v", i, result, err)
		}
	}
	if s.ImportCalls() != 1 {
		t.Fatal("competing sibling reached execution importer")
	}
	if _, err := f.Chain.InsertPerformanceExtension(ctx, b); !errors.Is(err, internal.ErrPerformanceParentNotHead) {
		t.Fatalf("typed import bypassed guard: %v", err)
	}
	if err := f.Chain.DB().View(ctx, func(tx kv.Tx) error {
		stored, err := rawdb.ReadBlockByHash(tx, b.Hash())
		if err != nil {
			return err
		}
		if stored != nil {
			t.Fatal("rejected sibling persisted in executor")
		}
		_, applied, _, err := rawdb.ReadQMDBApplied(tx)
		if applied != a.Hash() {
			t.Fatal("sibling changed actual applied marker")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result, err := c.AppliedChild(ctx, AppliedChildRequest{Identity: s.Identity(), ParentHash: a.ParentHash(), Number: 1, View: 2})
	if err != nil || result.Executed == nil || result.Executed.BlockHash != a.Hash() {
		t.Fatalf("wrong single-lineage child: %+v %v", result.Executed, err)
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, string(bytes.Repeat([]byte{'x'}, 4097))} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/applied-child", bytes.NewBufferString(body)))
		if w.Code == http.StatusOK {
			t.Fatal("invalid query framing accepted")
		}
	}
}

func TestAppliedChildRejectsFutureHeaderView(t *testing.T) {
	s, c, b := buildAndImportViewTwo(t)
	request := AppliedChildRequest{Identity: s.Identity(), ParentHash: b.ParentHash(), Number: 1, View: 1}
	if _, err := c.AppliedChild(context.Background(), request); err == nil {
		t.Fatal("later sealed header was offered for an earlier proposal view")
	}
	request.View = 2
	if _, err := c.AppliedChild(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}
