// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	v11 "github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// The v1.1 sync wire vectors (N42-SYNC-SPEC.md §3, §5.1-§5.3) are produced
// by the gov5 servers over the deterministic profile-1 chain of
// buildSnapshotJoinFixture and checked against testdata/sync-v11. Set
// N42_SYNC_V11_UPDATE=1 to rewrite the golden files and
// N42_SYNC_V11_VECTORS_OUT=<dir> to also publish them there.

type vecChunk struct {
	Code    string                 `json:"code"`
	Digest  string                 `json:"context_bytes,omitempty"`
	Payload string                 `json:"payload"`
	End     map[string]interface{} `json:"end,omitempty"`
	Error   string                 `json:"error_message,omitempty"`
}

type vecCase struct {
	Name          string                 `json:"name"`
	Note          string                 `json:"note"`
	Request       map[string]interface{} `json:"request_fields"`
	RequestHex    string                 `json:"request_payload"`
	RequestWire   string                 `json:"request_wire"`
	ResponseWire  string                 `json:"response_wire"`
	ResponseChunk []vecChunk             `json:"response_chunks"`
}

type vecFile struct {
	Protocol    string    `json:"protocol"`
	Spec        string    `json:"spec"`
	Framing     string    `json:"framing"`
	ChainID     uint64    `json:"chain_id"`
	GenesisHash string    `json:"genesis_hash"`
	Chain       string    `json:"chain"`
	Cases       []vecCase `json:"cases"`
}

const vecFraming = "request_wire = uvarint(len(request_payload)) || snappy framed(request_payload). " +
	"Each response chunk = result code (1 byte) || [4 context bytes = genesis_hash[:4], only on SUCCESS chunks of bodies_by_range/2] || " +
	"uvarint(len(payload)) || snappy framed(payload). Error chunks carry a UTF-8 message as payload. " +
	"Snappy framing is golang/snappy's buffered writer; an encoder that chunks differently produces other bytes for the same payload, " +
	"so implementations should compare payloads (and code/context bytes), and may compare whole wires only with the same framer."

func hx(b []byte) string { return "0x" + hex.EncodeToString(b) }

// decodeReply splits a reply into chunks for the vector file.
func decodeReply(t *testing.T, proto string, reply []byte, twigsEnd bool) []vecChunk {
	t.Helper()
	r := bytes.NewReader(reply)
	var out []vecChunk
	for r.Len() > 0 {
		code := reply[len(reply)-r.Len()]
		digest := proto == syncwire.ProtocolBodiesByRangeV2 && code == syncwire.CodeSuccess
		c, err := syncwire.ReadChunk(r, digest, 1<<30)
		if err != nil {
			t.Fatalf("%s: %v", proto, err)
		}
		vc := vecChunk{Code: fmt.Sprintf("0x%02x", c.Code), Payload: hx(c.Payload)}
		if digest {
			vc.Digest = hx(c.Digest[:])
		}
		switch {
		case c.Code == syncwire.CodeEnd && !twigsEnd:
			e, err := syncwire.DecodeRangeEnd(c.Payload)
			if err != nil {
				t.Fatal(err)
			}
			vc.End = map[string]interface{}{"next": e.Next, "served_bytes": e.ServedBytes, "reason": e.Reason}
		case c.Code == syncwire.CodeEnd:
			e, err := syncwire.DecodeTwigsEnd(c.Payload)
			if err != nil {
				t.Fatal(err)
			}
			vc.End = map[string]interface{}{"next_twig": e.NextTwig, "next_slot": e.NextSlot, "reason": e.Reason}
		case c.Code != syncwire.CodeSuccess:
			vc.Error = string(c.Payload)
		}
		out = append(out, vc)
	}
	return out
}

func TestSyncV11Vectors(t *testing.T) {
	f := buildSnapshotJoinFixture(t)
	resp, node := stateSyncServerNode(t, f)
	genesis := f.genesis.Hash()

	// A synthetic forest for the paging cases: 3 twigs, the first hollow,
	// labelled pivot 8 of this chain under another hash.
	tree := qmdb.New()
	key := func(i int) qmdb.Hash { return qmdb.Hash(syncwire.KeyHash([]byte(fmt.Sprintf("vector-key-%d", i)))) }
	n := 2*syncwire.TwigSize + 100
	for i := 0; i < n; i++ {
		tree.Set(key(i), []byte(fmt.Sprintf("value-%d", i)))
	}
	for i := 0; i < syncwire.TwigSize; i++ {
		tree.Delete(key(i))
	}
	for i := syncwire.TwigSize; i < n; i += 5 {
		tree.Delete(key(i))
	}
	synthHash := types.HexToHash("0x5157000000000000000000000000000000000000000000000000000000000008")
	var buf bytes.Buffer
	if _, err := qmdb.WritePortableSnapshotTombstone(&buf, qmdb.PortableSnapshotMetadata{
		ChainID: f.cfg.ChainID.Uint64(), GenesisHash: qmdb.Hash(genesis), BlockNumber: 8, BlockHash: qmdb.Hash(synthHash),
		Root: tree.Root(), NextSlot: tree.NextSlot(),
	}, tree, nil); err != nil {
		t.Fatal(err)
	}
	ph, twigs, err := syncwire.TwigsFromPortable(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	synthSrv := v11.NewServer(node.db, f.cfg, genesis)
	synthSrv.Publish(v11.NewSnapshot(ph.BlockNumber, types.Hash(ph.BlockHash), types.Hash(ph.Root), ph.NextSlot, twigs))
	synth := &v11.Responder{State: synthSrv}

	real := resp.State.Current()
	pivotHash := real.PivotHash
	committed := resp.Range.CommittedHead()
	blockLen := func(i int) uint64 { return uint64(len(mustEncode(t, f.blocks[i]))) }
	codeHash := firstCodeHash(t, node)

	type spec struct {
		name, note string
		on         *v11.Responder
		payload    []byte
		fields     map[string]interface{}
	}
	rangeReq := func(start, count, maxBytes, flags uint64) ([]byte, map[string]interface{}) {
		r := syncwire.RangeRequest{Start: start, Count: count, MaxBytes: maxBytes, Flags: flags}
		return r.Encode(), map[string]interface{}{"start": start, "count": count, "max_bytes": maxBytes, "flags": flags}
	}
	rng := func(name, note string, start, count, maxBytes, flags uint64) spec {
		p, fl := rangeReq(start, count, maxBytes, flags)
		return spec{name, note, resp, p, fl}
	}
	badRange, badRangeFields := rangeReq(1, 0, 0, 0)
	qcReq := func(number uint64, h types.Hash) ([]byte, map[string]interface{}) {
		return syncwire.CommitQCRequest{Number: number, Hash: h}.Encode(), map[string]interface{}{"number": number, "hash": h.Hex()}
	}
	qc := func(name, note string, number uint64, h types.Hash) spec {
		p, fl := qcReq(number, h)
		return spec{name, note, resp, p, fl}
	}
	man := func(name, note string, on *v11.Responder, pivot types.Hash, first uint64, count uint32) spec {
		r := syncwire.ManifestRequest{PivotHash: pivot, FirstTwig: first, Count: count}
		return spec{name, note, on, r.Encode(), map[string]interface{}{"pivot_hash": pivot.Hex(), "first_twig": first, "count": count, "reserved": 0}}
	}
	tw := func(name, note string, on *v11.Responder, pivot types.Hash, first uint64, count uint32, from uint64, flags, maxBytes uint32) spec {
		r := syncwire.TwigsRequest{PivotHash: pivot, FirstTwig: first, TwigCount: count, FromSlot: from, Flags: flags, MaxBytes: maxBytes}
		return spec{name, note, on, r.Encode(), map[string]interface{}{"pivot_hash": pivot.Hex(), "first_twig": first, "twig_count": count, "from_slot": from, "flags": flags, "max_bytes": maxBytes}}
	}
	code := func(name, note string, hs ...types.Hash) spec {
		var w []syncwire.Hash
		var hexes []string
		for _, h := range hs {
			w = append(w, syncwire.Hash(h))
			hexes = append(hexes, h.Hex())
		}
		p, err := syncwire.EncodeCodeRequest(w)
		if err != nil {
			p = []byte{0, 0}
		}
		return spec{name, note, resp, p, map[string]interface{}{"n": len(hs), "hashes": hexes}}
	}
	noCode, _ := hex.DecodeString("0000")

	files := []struct {
		file, proto, section string
		twigsEnd             bool
		cases                []spec
	}{
		{"bodies_by_range_2.json", syncwire.ProtocolBodiesByRangeV2, "§5.1", false, []spec{
			rng("complete", "bodies of 7..9: three blocks and END reason 0 (COMPLETE)", 7, 3, 0, 0),
			rng("headers_only", "HEADERS_ONLY over 6..9: four RLP headers", 6, 4, 0, syncwire.FlagHeadersOnly),
			rng("byte_budget", fmt.Sprintf("max_bytes = len(block 7) + 1 = %d: the first block is always sent, block 8 would overflow, END reason 1 (BYTE_BUDGET)", blockLen(6)+1), 7, 3, blockLen(6)+1, 0),
			rng("canonical_end", fmt.Sprintf("8..12 with the committed head at %d: END reason 3 (CANONICAL_END) after block %d", committed, committed), 8, 5, 0, 0),
			rng("empty_range", fmt.Sprintf("start above the committed head %d: zero chunks, END next = start, reason 3", committed), committed+1, 4, 0, 0),
			rng("not_available", "bodies below the earliest held body (the server joined at pivot 6): zero chunks, END reason 4 (NOT_AVAILABLE)", 2, 2, 0, 0),
			{"invalid_count_zero", "count 0 is refused with 0x01 INVALID_REQUEST", resp, badRange, badRangeFields},
		}},
		{"commit_qc_1.json", syncwire.ProtocolCommitQC, "§5.2", false, []spec{
			qc("by_number", "number 9 (the pivot's P+1), hash 0: the lowest commit QC reaching 9 and the headers from its block down to 9", 9, types.Hash{}),
			qc("by_hash", "number and hash of block 9", 9, f.blocks[8].Hash()),
			qc("highest", "number 0, hash 0: the highest commit QC the server holds and its block's header", 0, types.Hash{}),
			qc("unknown_hash", "a hash the server does not know: 0x03 RESOURCE_UNAVAILABLE", 5, types.HexToHash("0x77")),
			qc("above_head", "a number above the committed head: 0x03", committed+5, types.Hash{}),
		}},
		{"qmdb_status_1.json", syncwire.ProtocolQMDBStatus, "§5.3", false, []spec{
			{"status", "empty request; earliest_body, pivot_lo = pivot_hi = 8, pivot_hi_hash, flags 3 (twigs | code)", resp, nil, map[string]interface{}{}},
			{"non_empty_request", "any request bytes are refused with 0x01", resp, []byte{1}, map[string]interface{}{"payload": "0x01"}},
		}},
		{"qmdb_manifest_1.json", syncwire.ProtocolQMDBManifest, "§5.3", false, []spec{
			man("chain_pivot", "the real pivot 8 of the fixture chain: one twig, no proof (the twig root is the root)", resp, pivotHash, 0, 16),
			man("synthetic_middle", "synthetic 3-twig forest: twig 1 alone, range proof bottom-up, left sibling before right", synth, synthHash, 1, 1),
			man("synthetic_all", "synthetic 3-twig forest: all twigs, the zero padding leaf folded in", synth, synthHash, 0, 16384),
			man("out_of_range", "first_twig = twig_count: 0x01", synth, synthHash, 3, 1),
			man("unknown_pivot", "a pivot hash the server does not serve: 0x03", synth, types.HexToHash("0xee"), 0, 1),
		}},
		{"qmdb_twigs_1.json", syncwire.ProtocolQMDBTwigs, "§5.3", true, []spec{
			tw("chain_twig", "the real pivot's only twig: LIVE_P records (kind 4, preimage 20 or 52 bytes), DEAD records (kind 2); END reason 0", resp, pivotHash, 0, 1, 0, 0, 0),
			tw("hollow_and_live", "synthetic: twig 0 is hollow (kind 3: u16 2048 || leaf_root), twig 1 LIVE (kind 1, no preimage known) and DEAD", synth, synthHash, 0, 2, 0, 0, 0),
			tw("bits_only", "BITS_ONLY: the 256-byte active bitmaps and k = 0", synth, synthHash, 0, 3, 0, syncwire.FlagBitsOnly, 0),
			tw("byte_budget", "max_bytes = 1: the first twig is always sent, END reason 1 at next_twig 2", synth, synthHash, 1, 2, 0, 0, 1),
			tw("past_end", "twig_count runs past twig_count of the forest: END reason 4 (NOT_AVAILABLE) at next_twig 3", synth, synthHash, 2, 5, 0, 0, 0),
			tw("from_slot", "from_slot inside twig 2: records start at from_slot", synth, synthHash, 2, 1, 2*syncwire.TwigSize+40, 0, 0),
			tw("out_of_range", "first_twig = twig_count: 0x01", synth, synthHash, 3, 1, 0, 0, 0),
			tw("unknown_pivot", "a pivot hash the server does not serve: 0x03", synth, types.HexToHash("0xee"), 0, 1, 0, 0, 0),
		}},
		{"code_by_hash_1.json", syncwire.ProtocolCodeByHash, "§5.3", false, []spec{
			code("hit_and_miss", "a stored code hash and an unknown one: one SUCCESS chunk with the code, one 0x03 \"missing\"", codeHash, types.HexToHash("0x99")),
			{"empty_request", "n = 0 is refused with 0x01", resp, noCode, map[string]interface{}{"n": 0}},
		}},
	}

	golden := filepath.Join("testdata", "sync-v11")
	update := os.Getenv("N42_SYNC_V11_UPDATE") == "1"
	out := map[string][]byte{}
	for _, file := range files {
		vf := vecFile{Protocol: file.proto, Spec: "N42-SYNC-SPEC.md " + file.section + " (and §3 framing)", Framing: vecFraming,
			ChainID: f.cfg.ChainID.Uint64(), GenesisHash: genesis.Hex(),
			Chain: "gov5 internal/snapshot_join_test.go buildSnapshotJoinFixture: profile-1 v1 genesis, 11 blocks; server joined at pivot 6, applied 7..11, committed head 10, state-sync pivot 8 (root = header(9).Root)"}
		for _, c := range file.cases {
			reply := c.on.Respond(file.proto, c.payload)
			vf.Cases = append(vf.Cases, vecCase{
				Name: c.name, Note: c.note, Request: c.fields, RequestHex: hx(c.payload), RequestWire: hx(syncwire.AppendFramed(nil, c.payload)),
				ResponseWire: hx(reply), ResponseChunk: decodeReply(t, file.proto, reply, file.twigsEnd),
			})
		}
		data, err := json.MarshalIndent(vf, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		out[file.file] = append(data, '\n')
	}
	out["README.md"] = vectorsReadme(out)

	names := make([]string, 0, len(out))
	for name := range out {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(golden, name)
		if update {
			if err := os.MkdirAll(golden, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, out[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("golden %s: %v (run with N42_SYNC_V11_UPDATE=1)", name, err)
		}
		if !bytes.Equal(want, out[name]) {
			t.Fatalf("vector %s differs from the golden file (run with N42_SYNC_V11_UPDATE=1 after reviewing the change)", name)
		}
	}
	if dir := os.Getenv("N42_SYNC_V11_VECTORS_OUT"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), out[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func vectorsReadme(files map[string][]byte) []byte {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(`# N42 sync v1.1 wire vectors (gov5 reference)

Request/response vectors for the v1.1 sync protocols of N42-SYNC-SPEC.md:
bodies_by_range/2 (§5.1), commit_qc/1 (§5.2), qmdb_status/1,
qmdb_manifest/1, qmdb_twigs/1 and code_by_hash/1 (§5.3), with the §3
framing. Each file holds one protocol; each case has the decoded request
fields, the request payload and wire, the full response wire and its chunks
(result code, context bytes, payload, decoded END trailer or error message).

They are produced by gov5's servers (internal/sync/v11) over a deterministic
profile-1 chain and are reproducible from the gov5 branch feat/p1-sync-v11:

    N42_SYNC_V11_VECTORS_OUT=<dir> go test -tags nosqlite,noboltdb ./internal/ -run TestSyncV11Vectors

The same test checks them against internal/testdata/sync-v11 (golden).

Covered: SUCCESS, 0x01 INVALID_REQUEST, 0x03 RESOURCE_UNAVAILABLE, END
reasons 0 COMPLETE, 1 BYTE_BUDGET, 3 CANONICAL_END, 4 NOT_AVAILABLE, an
empty range (zero chunks), HEADERS_ONLY, BITS_ONLY, from_slot, hollow twigs,
LIVE (kind 1), LIVE_P (kind 4), DEAD (kind 2) and HOLLOW (kind 3) records,
and a manifest range proof. 0x04 RATE_LIMITED is the error chunk
0x04 || framed("1000") (retry_after_ms) and has no separate case.

## sha256

`)
	for _, n := range names {
		if n == "README.md" {
			continue
		}
		sum := sha256.Sum256(files[n])
		fmt.Fprintf(&b, "    %s  %s\n", hex.EncodeToString(sum[:]), n)
	}
	return []byte(b.String())
}

// firstCodeHash returns the lowest code hash stored on n.
func firstCodeHash(t *testing.T, n *p1Node) types.Hash {
	t.Helper()
	var h types.Hash
	if err := n.db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.Code)
		if err != nil {
			return err
		}
		defer c.Close()
		k, _, err := c.First()
		if err != nil || k == nil {
			return fmt.Errorf("no code stored: %v", err)
		}
		h = types.BytesToHash(k)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return h
}
