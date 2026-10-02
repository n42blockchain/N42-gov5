package vm

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// eip2537Vector mirrors the official go-ethereum EIP-2537 precompile fixture
// shape (Input/Expected/Gas/Name for success vectors, ExpectedError for the
// fail-* siblings).
type eip2537Vector struct {
	Input         string `json:"Input"`
	Expected      string `json:"Expected"`
	Gas           uint64 `json:"Gas"`
	Name          string `json:"Name"`
	ExpectedError string `json:"ExpectedError"`
	NoBenchmark   bool   `json:"NoBenchmark"`
}

func loadEip2537Vectors(t *testing.T, filename string) []eip2537Vector {
	t.Helper()

	path := filepath.Join("testdata", "eip2537", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	var vectors []eip2537Vector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("failed to unmarshal %s: %v", path, err)
	}
	return vectors
}

func runEip2537SuccessVectors(t *testing.T, filename string, pc PrecompiledContract) {
	t.Helper()

	for _, v := range loadEip2537Vectors(t, filename) {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			input, err := hex.DecodeString(v.Input)
			if err != nil {
				t.Fatalf("bad input hex: %v", err)
			}

			got, err := pc.Run(input)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			want, err := hex.DecodeString(v.Expected)
			if err != nil {
				t.Fatalf("bad expected hex: %v", err)
			}

			if hex.EncodeToString(got) != hex.EncodeToString(want) {
				t.Fatalf("vector %s: output mismatch\n got:  %x\n want: %x", v.Name, got, want)
			}

			if gas := pc.RequiredGas(input); gas != v.Gas {
				t.Fatalf("vector %s: RequiredGas() = %d, want %d", v.Name, gas, v.Gas)
			}
		})
	}
}

func runEip2537FailVectors(t *testing.T, filename string, pc PrecompiledContract) {
	t.Helper()

	for _, v := range loadEip2537Vectors(t, filename) {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			input, err := hex.DecodeString(v.Input)
			if err != nil {
				t.Fatalf("bad input hex: %v", err)
			}

			if _, err := pc.Run(input); err == nil {
				t.Fatalf("vector %s: Run() succeeded, want error %q", v.Name, v.ExpectedError)
			}
		})
	}
}

func TestEIP2537BLS12381G1Add(t *testing.T) {
	t.Parallel()
	pc := &bls12381G1Add{}
	runEip2537SuccessVectors(t, "blsG1Add.json", pc)
	runEip2537FailVectors(t, "fail-blsG1Add.json", pc)
}

func TestEIP2537BLS12381G1Mul(t *testing.T) {
	t.Parallel()
	pc := &bls12381G1Mul{}
	runEip2537SuccessVectors(t, "blsG1Mul.json", pc)
	runEip2537FailVectors(t, "fail-blsG1Mul.json", pc)
}

func TestEIP2537BLS12381G1MultiExp(t *testing.T) {
	t.Parallel()
	pc := &bls12381G1MultiExp{}
	runEip2537SuccessVectors(t, "blsG1MultiExp.json", pc)
	runEip2537FailVectors(t, "fail-blsG1MultiExp.json", pc)
}

func TestEIP2537BLS12381G2Add(t *testing.T) {
	t.Parallel()
	pc := &bls12381G2Add{}
	runEip2537SuccessVectors(t, "blsG2Add.json", pc)
	runEip2537FailVectors(t, "fail-blsG2Add.json", pc)
}

func TestEIP2537BLS12381G2Mul(t *testing.T) {
	t.Parallel()
	pc := &bls12381G2Mul{}
	runEip2537SuccessVectors(t, "blsG2Mul.json", pc)
	runEip2537FailVectors(t, "fail-blsG2Mul.json", pc)
}

func TestEIP2537BLS12381G2MultiExp(t *testing.T) {
	t.Parallel()
	pc := &bls12381G2MultiExp{}
	runEip2537SuccessVectors(t, "blsG2MultiExp.json", pc)
	runEip2537FailVectors(t, "fail-blsG2MultiExp.json", pc)
}

func TestEIP2537BLS12381Pairing(t *testing.T) {
	t.Parallel()
	pc := &bls12381Pairing{}
	runEip2537SuccessVectors(t, "blsPairing.json", pc)
	runEip2537FailVectors(t, "fail-blsPairing.json", pc)
}

func TestEIP2537BLS12381MapG1(t *testing.T) {
	t.Parallel()
	pc := &bls12381MapG1{}
	runEip2537SuccessVectors(t, "blsMapG1.json", pc)
	runEip2537FailVectors(t, "fail-blsMapG1.json", pc)
}

func TestEIP2537BLS12381MapG2(t *testing.T) {
	t.Parallel()
	pc := &bls12381MapG2{}
	runEip2537SuccessVectors(t, "blsMapG2.json", pc)
	runEip2537FailVectors(t, "fail-blsMapG2.json", pc)
}
