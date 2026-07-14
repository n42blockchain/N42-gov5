// Command n42-blspool generates a simulated mobile-voter BLS key pool for the
// chain re-seal conversion. Keys are derived deterministically from a single
// random master seed (printed in the output), so the entire pool can be
// regenerated from that seed alone. The output is a Markdown document that
// contains the master seed plus every derived public/private key.
//
// SECURITY: the output contains PRIVATE KEYS. Never commit it to git. Keep
// local backups only (the tool writes to every --out path for dual backup).
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
)

func main() {
	count := flag.Int("count", 200000, "number of BLS keypairs to generate")
	outCSV := flag.String("out", "", "comma-separated output .md paths (each receives a full copy for backup)")
	seedHex := flag.String("seed", "", "32-byte hex master seed (empty = random)")
	flag.Parse()

	if *outCSV == "" {
		fmt.Fprintln(os.Stderr, "error: --out is required (comma-separated .md paths for dual backup)")
		os.Exit(1)
	}
	outs := strings.Split(*outCSV, ",")

	// Master seed: deterministic regeneration is possible from this alone.
	var seed [32]byte
	if *seedHex != "" {
		b, err := hex.DecodeString(strings.TrimPrefix(*seedHex, "0x"))
		if err != nil || len(b) != 32 {
			fmt.Fprintln(os.Stderr, "error: --seed must be 32-byte hex")
			os.Exit(1)
		}
		copy(seed[:], b)
	} else {
		if _, err := rand.Read(seed[:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: seed: %v\n", err)
			os.Exit(1)
		}
	}

	start := time.Now()
	fmt.Printf("Generating %d BLS keypairs (deterministic from master seed)...\n", *count)

	type kp struct{ pub, priv string }
	kps := make([]kp, *count)
	for i := 0; i < *count; i++ {
		// ikm_i = sha256(seed || i) — deterministic per index.
		var idx [8]byte
		binary.BigEndian.PutUint64(idx[:], uint64(i))
		ikm := sha256.Sum256(append(append([]byte{}, seed[:]...), idx[:]...))
		sk, err := bls.SecretKeyFromRandom32Byte(ikm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: derive key %d: %v\n", i, err)
			os.Exit(1)
		}
		pub := sk.PublicKey().Marshal()
		kps[i] = kp{pub: hex.EncodeToString(pub), priv: hex.EncodeToString(sk.Marshal())}
		if (i+1)%50000 == 0 {
			fmt.Printf("  ... %d/%d\n", i+1, *count)
		}
	}
	fmt.Printf("Derived %d keys in %s\n", *count, time.Since(start))

	// Validator address = last 20 bytes of keccak256(pubkey) — a stable identity.
	addrOf := func(pubHex string) string {
		pub, _ := hex.DecodeString(pubHex)
		h := crypto.Keccak256(pub)
		return "0x" + hex.EncodeToString(h[12:])
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05 MST")
	for _, out := range outs {
		out = strings.TrimSpace(out)
		if out == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", out, err)
			os.Exit(1)
		}
		f, err := openPrivateOutput(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: create %s: %v\n", out, err)
			os.Exit(1)
		}
		w := bufio.NewWriterSize(f, 1<<20)
		fmt.Fprintf(w, "# N42 Mobile BLS Voting Pool (Simulated)\n\n")
		fmt.Fprintf(w, "- Generated: %s\n", now)
		fmt.Fprintf(w, "- Count: %d\n", *count)
		fmt.Fprintf(w, "- Curve: BLS12-381 (blst); pubkey 48B (G1 compressed), privkey 32B, signature 96B (G2)\n")
		fmt.Fprintf(w, "- Derivation: privkey_i = blsKeygen(sha256(seed || uint64_be(i))) — regenerable from the master seed below\n")
		fmt.Fprintf(w, "- Purpose: simulated mobile-voter pool for chain re-seal conversion (committee size 512, ETH sync-committee reference)\n")
		fmt.Fprintf(w, "- **SECURITY: contains PRIVATE KEYS. Never commit to git. Local backups only.**\n\n")
		fmt.Fprintf(w, "## Master seed\n\n")
		fmt.Fprintf(w, "```\nseed = 0x%s\n```\n\n", hex.EncodeToString(seed[:]))
		fmt.Fprintf(w, "## Keys\n\n")
		fmt.Fprintf(w, "Format: `index,address,pubkey,privkey`\n\n```\n")
		for i := range kps {
			fmt.Fprintf(w, "%d,%s,%s,%s\n", i, addrOf(kps[i].pub), kps[i].pub, kps[i].priv)
		}
		fmt.Fprintf(w, "```\n")
		if err := w.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", out, err)
			os.Exit(1)
		}
		f.Close()
		fi, _ := os.Stat(out)
		fmt.Printf("Wrote %s (%.1f MB)\n", out, float64(fi.Size())/1e6)
	}
	fmt.Printf("Done in %s. Master seed (KEEP SAFE): 0x%s\n", time.Since(start), hex.EncodeToString(seed[:]))
}

func openPrivateOutput(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	// OpenFile preserves an existing file's mode. Force private-key material
	// back to owner-only permissions even when overwriting an older output.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
