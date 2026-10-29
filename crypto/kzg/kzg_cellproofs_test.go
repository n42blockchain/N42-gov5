package kzg

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// cellProofVector is one consensus-spec compute_cells_and_kzg_proofs case:
// a blob and, when valid, its 128 cells and 128 cell proofs.
type cellProofVector struct {
	name   string
	blob   *Blob
	proofs []Proof // nil for an invalid-blob case
}

// loadCellProofVectors reads the EIP-7594 consensus-spec vectors
// (tests/compute_cells_and_kzg_proofs/kzg-mainnet) shipped inside the
// go-eth-kzg module this package already depends on, so the vectors are the
// exact version the library is tested against without copying ~10 MB of
// YAML into the repo.
func loadCellProofVectors(t *testing.T) []cellProofVector {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/crate-crypto/go-eth-kzg").Output()
	if err != nil {
		t.Skipf("go-eth-kzg module directory unavailable: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "tests", "compute_cells_and_kzg_proofs", "kzg-mainnet")
	files, err := filepath.Glob(filepath.Join(dir, "*", "data.yaml"))
	if err != nil || len(files) == 0 {
		t.Skipf("no compute_cells_and_kzg_proofs vectors under %s", dir)
	}
	var vectors []cellProofVector
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		var test struct {
			Input struct {
				Blob string `yaml:"blob"`
			} `yaml:"input"`
			Output *[][]string `yaml:"output"`
		}
		if err := yaml.Unmarshal(raw, &test); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		v := cellProofVector{name: filepath.Base(filepath.Dir(file))}
		blobBytes, err := hex.DecodeString(strings.TrimPrefix(test.Input.Blob, "0x"))
		if err != nil || len(blobBytes) != BytesPerBlob {
			if test.Output != nil {
				t.Fatalf("%s: malformed blob in a valid case", v.name)
			}
			continue // wrong-length blob: not representable as a Blob
		}
		v.blob = new(Blob)
		copy(v.blob[:], blobBytes)
		if test.Output != nil {
			for _, p := range (*test.Output)[1] {
				b, err := hex.DecodeString(strings.TrimPrefix(p, "0x"))
				if err != nil || len(b) != BytesPerProof {
					t.Fatalf("%s: malformed proof", v.name)
				}
				var proof Proof
				copy(proof[:], b)
				v.proofs = append(v.proofs, proof)
			}
			if len(v.proofs) != CellsPerExtBlob {
				t.Fatalf("%s: %d proofs, want %d", v.name, len(v.proofs), CellsPerExtBlob)
			}
		}
		vectors = append(vectors, v)
	}
	return vectors
}

func validCellProofVectors(t *testing.T, n int) []cellProofVector {
	var valid []cellProofVector
	for _, v := range loadCellProofVectors(t) {
		// Skip degenerate blobs (e.g. all zero) whose cell proofs are all
		// identical: swapping proofs there proves nothing.
		if v.proofs != nil && v.proofs[0] != v.proofs[1] {
			valid = append(valid, v)
		}
		if len(valid) == n {
			break
		}
	}
	if len(valid) < n {
		t.Skipf("only %d valid vectors, need %d", len(valid), n)
	}
	return valid
}

// TestComputeCellProofsSpecVectors checks ComputeCellProofs against every
// consensus-spec compute_cells_and_kzg_proofs case and that VerifyCellProofs
// accepts exactly the official proofs.
func TestComputeCellProofsSpecVectors(t *testing.T) {
	for _, v := range loadCellProofVectors(t) {
		proofs, err := ComputeCellProofs(v.blob)
		if v.proofs == nil {
			if err == nil {
				t.Errorf("%s: invalid blob accepted", v.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: ComputeCellProofs: %v", v.name, err)
		}
		if len(proofs) != len(v.proofs) {
			t.Fatalf("%s: %d proofs, want %d", v.name, len(proofs), len(v.proofs))
		}
		for i := range proofs {
			if proofs[i] != v.proofs[i] {
				t.Fatalf("%s: proof %d differs from the spec vector", v.name, i)
			}
		}
		commitment, err := BlobToCommitment(v.blob)
		if err != nil {
			t.Fatalf("%s: commitment: %v", v.name, err)
		}
		if err := VerifyCellProofs([]Blob{*v.blob}, []Commitment{commitment}, v.proofs); err != nil {
			t.Fatalf("%s: VerifyCellProofs rejected the spec proofs: %v", v.name, err)
		}
	}
}

// TestVerifyCellProofsBatchRejectsTampering verifies a two-blob batch built
// from spec vectors and that swapping, truncating or mis-assigning proofs is
// rejected.
func TestVerifyCellProofsBatchRejectsTampering(t *testing.T) {
	vs := validCellProofVectors(t, 2)
	blobs := []Blob{*vs[0].blob, *vs[1].blob}
	commitments := make([]Commitment, 2)
	for i, v := range vs {
		c, err := BlobToCommitment(v.blob)
		if err != nil {
			t.Fatal(err)
		}
		commitments[i] = c
	}
	proofs := append(append([]Proof{}, vs[0].proofs...), vs[1].proofs...)
	if err := VerifyCellProofs(blobs, commitments, proofs); err != nil {
		t.Fatalf("valid two-blob batch rejected: %v", err)
	}

	swapped := append([]Proof{}, proofs...)
	swapped[0], swapped[1] = swapped[1], swapped[0]
	if err := VerifyCellProofs(blobs, commitments, swapped); err == nil {
		t.Fatal("swapped cell proofs accepted")
	}
	if err := VerifyCellProofs(blobs, commitments, proofs[:len(proofs)-1]); err == nil {
		t.Fatal("truncated cell proofs accepted")
	}
	crossed := append(append([]Proof{}, vs[1].proofs...), vs[0].proofs...)
	if err := VerifyCellProofs(blobs, commitments, crossed); err == nil {
		t.Fatal("proofs of the other blob accepted")
	}
	if err := VerifyCellProofs(blobs, []Commitment{commitments[1], commitments[0]}, proofs); err == nil {
		t.Fatal("swapped commitments accepted")
	}
}

func sidecarHashes(commitments []Commitment) []types.Hash {
	hashes := make([]types.Hash, len(commitments))
	for i, c := range commitments {
		hashes[i] = CommitmentToVersionedHash(c)
	}
	return hashes
}

// TestValidateBlobSidecarVersion1 checks the version-1 path of the sidecar
// validator and the fork rule: from Osaka only version 1, before it only
// version 0.
func TestValidateBlobSidecarVersion1(t *testing.T) {
	v := validCellProofVectors(t, 1)[0]
	commitment, err := BlobToCommitment(v.blob)
	if err != nil {
		t.Fatal(err)
	}
	blobProof, err := computeBlobProof(v.blob, commitment)
	if err != nil {
		t.Fatal(err)
	}
	hashes := sidecarHashes([]Commitment{commitment})
	v1 := &transaction.BlobTxSidecar{
		Version:     transaction.BlobSidecarVersion1,
		Blobs:       []Blob{*v.blob},
		Commitments: []Commitment{commitment},
		Proofs:      append([]Proof{}, v.proofs...),
	}
	v0 := &transaction.BlobTxSidecar{
		Version:     transaction.BlobSidecarVersion0,
		Blobs:       []Blob{*v.blob},
		Commitments: []Commitment{commitment},
		Proofs:      []Proof{blobProof},
	}
	if err := ValidateBlobSidecar(v1, hashes); err != nil {
		t.Fatalf("valid v1 sidecar rejected: %v", err)
	}
	if err := ValidateBlobSidecar(v0, hashes); err != nil {
		t.Fatalf("valid v0 sidecar rejected: %v", err)
	}

	// Fork rule.
	if err := ValidateBlobSidecarForFork(v1, hashes, true); err != nil {
		t.Fatalf("Osaka rejected a valid v1 sidecar: %v", err)
	}
	if err := ValidateBlobSidecarForFork(v0, hashes, true); err == nil {
		t.Fatal("Osaka accepted a version-0 sidecar")
	}
	if err := ValidateBlobSidecarForFork(v0, hashes, false); err != nil {
		t.Fatalf("pre-Osaka rejected a valid v0 sidecar: %v", err)
	}
	if err := ValidateBlobSidecarForFork(v1, hashes, false); err == nil {
		t.Fatal("pre-Osaka accepted a version-1 sidecar")
	}

	// A version-1 sidecar carrying a single blob proof, or a tampered cell
	// proof, must fail.
	wrongCount := v1.Copy()
	wrongCount.Proofs = []Proof{blobProof}
	if err := ValidateBlobSidecar(wrongCount, hashes); err == nil {
		t.Fatal("v1 sidecar with one proof per blob accepted")
	}
	tampered := v1.Copy()
	tampered.Proofs[127][10] ^= 0x01
	if err := ValidateBlobSidecar(tampered, hashes); err == nil {
		t.Fatal("v1 sidecar with a tampered cell proof accepted")
	}
	unknown := v1.Copy()
	unknown.Version = 2
	if err := ValidateBlobSidecar(unknown, hashes); err == nil {
		t.Fatal("sidecar version 2 accepted")
	}
}

// TestSidecarToV1 converts a version-0 sidecar and checks the result carries
// the spec cell proofs.
func TestSidecarToV1(t *testing.T) {
	v := validCellProofVectors(t, 1)[0]
	commitment, err := BlobToCommitment(v.blob)
	if err != nil {
		t.Fatal(err)
	}
	blobProof, err := computeBlobProof(v.blob, commitment)
	if err != nil {
		t.Fatal(err)
	}
	sc := &transaction.BlobTxSidecar{
		Blobs:       []Blob{*v.blob},
		Commitments: []Commitment{commitment},
		Proofs:      []Proof{blobProof},
	}
	if err := SidecarToV1(sc); err != nil {
		t.Fatalf("SidecarToV1: %v", err)
	}
	if sc.Version != transaction.BlobSidecarVersion1 || len(sc.Proofs) != CellsPerExtBlob {
		t.Fatalf("converted sidecar = version %d, %d proofs", sc.Version, len(sc.Proofs))
	}
	for i := range sc.Proofs {
		if sc.Proofs[i] != v.proofs[i] {
			t.Fatalf("converted proof %d differs from the spec vector", i)
		}
	}
	// Idempotent on a version-1 sidecar.
	if err := SidecarToV1(sc); err != nil || len(sc.Proofs) != CellsPerExtBlob {
		t.Fatalf("SidecarToV1 on v1 = %v, %d proofs", err, len(sc.Proofs))
	}
}

func computeBlobProof(blob *Blob, commitment Commitment) (Proof, error) {
	ctx, err := GetContext()
	if err != nil {
		return Proof{}, err
	}
	return ctx.ComputeBlobProof(blob, commitment)
}
