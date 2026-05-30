package stateless

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

// StatelessBundle is the per-block unit a producer ships to a minimal client:
// everything needed to verify block N's transition across all three trust
// layers with no full state.
//
//	① Header  — RLP header bytes; the parentHash chain anchors the block's
//	            stateRoot / receiptRoot (the verification targets).
//	② Body    — encoded transactions/withdrawals (EVM replay input);
//	   Witness — the length-prefixed v1 state-read stream for EVM replay;
//	   NewCode — bytecodes first seen in this block/window. The client caches
//	            them by keccak; code it already holds is omitted, and any older
//	            code still referenced is fetched on demand via CodeRequest.
//	③ Proof   — the MPT stateless multiproof + changeset (BlockProof).
//
// Layers ② and ③ are independent given the trusted header, so a window of
// bundles verifies in parallel (see VerifyBatch).
type StatelessBundle struct {
	Number  uint64
	Header  []byte
	Body    []byte
	Witness []byte
	NewCode [][]byte
	Proof   *BlockProof
}

// CodeRequest is a minimal client asking the producer for bytecodes it lacks —
// code referenced (by codeHash) in a bundle's changeset but neither shipped in
// NewCode nor already in the client's cache.
type CodeRequest struct {
	Hashes []types.Hash
}

// CodeResponse carries the requested bytecodes. Codes are content-addressed:
// the client MUST check each returned blob hashes to a requested codeHash
// (VerifyCodeResponse), so a server cannot substitute code.
type CodeResponse struct {
	Codes [][]byte
}

// MissingCodeHashes returns the codeHashes block N's changeset references that
// are neither shipped in NewCode nor already held by the client (have(hash) ==
// true). The empty-code hash (keccak(nil)) is never requested. The result is the
// payload for a CodeRequest.
func (b *StatelessBundle) MissingCodeHashes(have func(types.Hash) bool) []types.Hash {
	emptyCH := types.BytesToHash(emptyCodeHashBytes)
	shipped := make(map[types.Hash]struct{}, len(b.NewCode))
	for _, c := range b.NewCode {
		shipped[types.BytesToHash(keccak(c))] = struct{}{}
	}
	seen := map[types.Hash]struct{}{}
	var out []types.Hash
	if b.Proof == nil {
		return out
	}
	for i := range b.Proof.Changes {
		c := &b.Proof.Changes[i]
		if c.Deleted || len(c.CodeHash) != 32 {
			continue
		}
		var ch types.Hash
		copy(ch[:], c.CodeHash)
		if ch == emptyCH {
			continue
		}
		if _, ok := shipped[ch]; ok {
			continue
		}
		if have != nil && have(ch) {
			continue
		}
		if _, dup := seen[ch]; dup {
			continue
		}
		seen[ch] = struct{}{}
		out = append(out, ch)
	}
	return out
}

// VerifyCodeResponse checks every returned blob hashes to one of the requested
// hashes and returns the verified codeHash->code map. An unrequested or
// mis-hashed blob is an error (content-addressing makes substitution detectable).
func VerifyCodeResponse(req *CodeRequest, resp *CodeResponse) (map[types.Hash][]byte, error) {
	want := make(map[types.Hash]struct{}, len(req.Hashes))
	for _, h := range req.Hashes {
		want[h] = struct{}{}
	}
	out := make(map[types.Hash][]byte, len(resp.Codes))
	for i, c := range resp.Codes {
		h := types.BytesToHash(keccak(c))
		if _, ok := want[h]; !ok {
			return nil, fmt.Errorf("code response[%d]: keccak %x not requested", i, h[:6])
		}
		out[h] = append([]byte(nil), c...)
	}
	return out, nil
}

// --- wire codec (length-prefixed framing) ---

func putBytes(dst, b []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	dst = append(dst, n[:]...)
	return append(dst, b...)
}

func putList(dst []byte, l [][]byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(l)))
	dst = append(dst, n[:]...)
	for _, b := range l {
		dst = putBytes(dst, b)
	}
	return dst
}

type rdr struct {
	b   []byte
	pos int
}

func (r *rdr) bytes() ([]byte, error) {
	if r.pos+4 > len(r.b) {
		return nil, fmt.Errorf("bundle: truncated length at %d", r.pos)
	}
	n := int(binary.BigEndian.Uint32(r.b[r.pos:]))
	r.pos += 4
	if r.pos+n > len(r.b) {
		return nil, fmt.Errorf("bundle: truncated payload (want %d, have %d)", n, len(r.b)-r.pos)
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *rdr) list() ([][]byte, error) {
	if r.pos+4 > len(r.b) {
		return nil, fmt.Errorf("bundle: truncated list count at %d", r.pos)
	}
	n := int(binary.BigEndian.Uint32(r.b[r.pos:]))
	r.pos += 4
	out := make([][]byte, n)
	for i := 0; i < n; i++ {
		v, err := r.bytes()
		if err != nil {
			return nil, err
		}
		out[i] = append([]byte(nil), v...)
	}
	return out, nil
}

func (r *rdr) u64() (uint64, error) {
	if r.pos+8 > len(r.b) {
		return 0, fmt.Errorf("bundle: truncated u64 at %d", r.pos)
	}
	v := binary.BigEndian.Uint64(r.b[r.pos:])
	r.pos += 8
	return v, nil
}

func putU64(dst []byte, v uint64) []byte {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], v)
	return append(dst, n[:]...)
}

// EncodeBlockProof serializes a BlockProof (length-prefixed; the full RLP node
// sets, not the compact wire — correctness over size here).
func EncodeBlockProof(bp *BlockProof) []byte {
	dst := putU64(nil, bp.Number)
	dst = putList(dst, bp.AccountProof)
	var cnt [4]byte
	binary.BigEndian.PutUint32(cnt[:], uint32(len(bp.Changes)))
	dst = append(dst, cnt[:]...)
	for i := range bp.Changes {
		c := &bp.Changes[i]
		dst = putBytes(dst, c.AddrHash[:])
		dst = putU64(dst, c.Nonce)
		bal := c.Balance.Bytes()
		dst = putBytes(dst, bal)
		dst = putBytes(dst, c.CodeHash)
		dst = putBytes(dst, c.StorageRoot)
		dst = putList(dst, c.StorageProof)
		var scnt [4]byte
		binary.BigEndian.PutUint32(scnt[:], uint32(len(c.Storage)))
		dst = append(dst, scnt[:]...)
		for _, s := range c.Storage {
			dst = putBytes(dst, s.SlotHash[:])
			dst = putBytes(dst, s.Value)
		}
		if c.Deleted {
			dst = append(dst, 1)
		} else {
			dst = append(dst, 0)
		}
	}
	return dst
}

// DecodeBlockProof reverses EncodeBlockProof.
func DecodeBlockProof(b []byte) (*BlockProof, error) {
	r := &rdr{b: b}
	num, err := r.u64()
	if err != nil {
		return nil, err
	}
	ap, err := r.list()
	if err != nil {
		return nil, err
	}
	bp := &BlockProof{Number: num, AccountProof: ap}
	if r.pos+4 > len(r.b) {
		return nil, fmt.Errorf("bundle: truncated changes count")
	}
	nc := int(binary.BigEndian.Uint32(r.b[r.pos:]))
	r.pos += 4
	bp.Changes = make([]AccountChange, nc)
	for i := 0; i < nc; i++ {
		c := &bp.Changes[i]
		ah, err := r.bytes()
		if err != nil {
			return nil, err
		}
		copy(c.AddrHash[:], ah)
		if c.Nonce, err = r.u64(); err != nil {
			return nil, err
		}
		bal, err := r.bytes()
		if err != nil {
			return nil, err
		}
		c.Balance.SetBytes(bal)
		if c.CodeHash, err = r.bytes(); err != nil {
			return nil, err
		}
		c.CodeHash = append([]byte(nil), c.CodeHash...)
		if c.StorageRoot, err = r.bytes(); err != nil {
			return nil, err
		}
		c.StorageRoot = append([]byte(nil), c.StorageRoot...)
		if c.StorageProof, err = r.list(); err != nil {
			return nil, err
		}
		if r.pos+4 > len(r.b) {
			return nil, fmt.Errorf("bundle: truncated storage count")
		}
		ns := int(binary.BigEndian.Uint32(r.b[r.pos:]))
		r.pos += 4
		c.Storage = make([]StorageChange, ns)
		for j := 0; j < ns; j++ {
			sh, err := r.bytes()
			if err != nil {
				return nil, err
			}
			copy(c.Storage[j].SlotHash[:], sh)
			v, err := r.bytes()
			if err != nil {
				return nil, err
			}
			c.Storage[j].Value = append([]byte(nil), v...)
		}
		if r.pos >= len(r.b) {
			return nil, fmt.Errorf("bundle: truncated deleted flag")
		}
		c.Deleted = r.b[r.pos] == 1
		r.pos++
	}
	return bp, nil
}

// Encode serializes the bundle to a self-describing byte slice.
func (b *StatelessBundle) Encode() []byte {
	dst := putU64(nil, b.Number)
	dst = putBytes(dst, b.Header)
	dst = putBytes(dst, b.Body)
	dst = putBytes(dst, b.Witness)
	dst = putList(dst, b.NewCode)
	if b.Proof != nil {
		dst = putBytes(dst, EncodeBlockProof(b.Proof))
	} else {
		dst = putBytes(dst, nil)
	}
	return dst
}

// DecodeBundle reverses Encode. A zero-length proof section yields Proof == nil.
func DecodeBundle(raw []byte) (*StatelessBundle, error) {
	r := &rdr{b: raw}
	num, err := r.u64()
	if err != nil {
		return nil, err
	}
	b := &StatelessBundle{Number: num}
	if b.Header, err = r.bytes(); err != nil {
		return nil, err
	}
	b.Header = append([]byte(nil), b.Header...)
	if b.Body, err = r.bytes(); err != nil {
		return nil, err
	}
	b.Body = append([]byte(nil), b.Body...)
	if b.Witness, err = r.bytes(); err != nil {
		return nil, err
	}
	b.Witness = append([]byte(nil), b.Witness...)
	if b.NewCode, err = r.list(); err != nil {
		return nil, err
	}
	pb, err := r.bytes()
	if err != nil {
		return nil, err
	}
	if len(pb) > 0 {
		if b.Proof, err = DecodeBlockProof(pb); err != nil {
			return nil, err
		}
	}
	return b, nil
}
