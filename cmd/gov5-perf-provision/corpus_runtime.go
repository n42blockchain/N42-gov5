package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/holiman/uint256"
	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/params"
)

type supplyRuntimeBinding struct {
	Root              string     `json:"root"`
	ManifestSHA256    string     `json:"manifest_sha256"`
	CorpusID          types.Hash `json:"corpus_id"`
	GenesisFileSHA256 string     `json:"genesis_file_sha256"`
	BlockIntervalMS   uint64     `json:"block_interval_ms"`
}

func readPrivateCorpusSource(path string, limit int64) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("corpus source requires absolute path without aliases")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 1 || before.Size() > limit {
		return nil, errors.New("corpus source requires bounded private owned regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, errors.New("corpus source changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("corpus source grew beyond limit")
	}
	return raw, nil
}

// This opt-in path preserves the corpus's genesis bytes and BLS validator set.
// It does not read sender.seed, copy execution state or sign new transactions.
func provisionSupplyRuntime(root, source string, basePort int, interval uint64, allowedBase, supplyBase string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !strings.HasPrefix(root, allowedBase+string(filepath.Separator)) || !filepath.IsAbs(source) || filepath.Clean(source) != source || !strings.HasPrefix(source, supplyBase+string(filepath.Separator)) || basePort < 25000 || basePort > 29993 || interval > 60000 || len(filepath.Join(root, "v0", "validator-rpc", "validator.sock")) > 107 {
		return errors.New("explicit isolated supply/runtime paths, bounded ports and pacing required")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	resolved, resolveErr := filepath.EvalSymlinks(source)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !info.IsDir() || info.Mode().Perm() != 0700 || resolveErr != nil || resolved != source {
		return errors.New("supply directory must be private, owned and unaliased")
	}
	manifestRaw, err := readPrivateCorpusSource(filepath.Join(source, "manifest.json"), sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return err
	}
	corpus, err := sharedexec.DecodeCorpusManifest(manifestRaw)
	if err != nil {
		return err
	}
	genesisRaw, err := readPrivateCorpusSource(filepath.Join(source, "genesis.json"), 16<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(genesisRaw)
	if types.Hash(sum) != corpus.Descriptor.GenesisFileHash {
		return errors.New("supply genesis file SHA differs from manifest")
	}
	var g conf.Genesis
	if err := json.Unmarshal(genesisRaw, &g); err != nil {
		return err
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if g.Config == nil || g.Config.Performance == nil || g.Config.HotStuff == nil || g.Config.ValidatePerformanceSelection("private", profile) != nil || g.Config.DeferredExecutionTime != nil || g.Config.HotStuff.InteropV4 || g.Config.HotStuff.CommitteePool != nil || len(g.Config.HotStuff.Validators) != 7 || g.Timestamp != params.PerformanceGenesisTime || g.Number != 0 || !bytes.Equal(g.ExtraData, []byte(params.PerformanceGenesisMarker)) || len(g.Alloc) != int(corpus.Descriptor.SenderCount) || corpus.Descriptor.NonceStart != 0 || g.BaseFee == nil || !g.BaseFee.IsUint64() || g.BaseFee.Uint64() != 1 {
		return errors.New("unsupported explicit supply genesis/validator/nonce shape")
	}
	for _, account := range g.Alloc {
		balance, err := uint256.FromHex(account.Balance)
		if account.Nonce != 0 || len(account.Code) != 0 || len(account.Storage) != 0 || err != nil || balance.Cmp(uint256.NewInt(corpus.Descriptor.NoncesPerSender*21001)) < 0 {
			return errors.New("supply genesis requires funded nonce-zero senders")
		}
	}
	gb, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		return err
	}
	identity := sharedexec.Identity{Version: 1, Namespace: g.Config.Performance.NetworkNamespace, ChainID: g.Config.ChainID.String(), GenesisHash: gb.Hash()}
	if identity != corpus.Descriptor.Identity {
		return errors.New("supply actual genesis identity differs")
	}
	// Validate all seven source keys before creating anything at the new root.
	// Each private key goes only to its matching validator, never the owner.
	privateKeys := make([][]byte, 7)
	seen := make(map[types.Address]bool)
	for i, validator := range g.Config.HotStuff.Validators {
		address := types.HexToAddress(validator.Address)
		if address == (types.Address{}) || !strings.EqualFold(validator.Address, address.Hex()) || seen[address] {
			return errors.New("invalid duplicate supply validator")
		}
		seen[address] = true
		name := "bls_" + strings.ToLower(address.Hex()) + ".key"
		raw, err := readPrivateCorpusSource(filepath.Join(source, fmt.Sprintf("v%d", i), "keystore", name), 256)
		if err != nil {
			return err
		}
		encoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return errors.New("invalid supply BLS encoding")
		}
		key, err := bls.SecretKeyFromBytes(encoded)
		if err != nil {
			return err
		}
		public := key.PublicKey().Marshal()
		if "0x"+hex.EncodeToString(public) != strings.ToLower(validator.BLSKey) || types.BytesToAddress(crypto.Keccak256(public)[12:]) != address {
			return errors.New("supply BLS key/order/public address mismatch")
		}
		privateKeys[i] = raw
	}
	parent := filepath.Dir(root)
	ancestor := parent
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || filepath.Dir(ancestor) == ancestor {
			return errors.New("runtime ancestry unavailable")
		}
		ancestor = filepath.Dir(ancestor)
	}
	if resolved, err := filepath.EvalSymlinks(ancestor); err != nil || resolved != ancestor {
		return errors.New("runtime ancestry cannot contain aliases")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(parent); err != nil || resolved != parent {
		return errors.New("runtime parent cannot contain aliases")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("fresh supply runtime root required: %w", err)
	}
	// Errors leave this private partial runtime intact for review/recovery.
	out := manifest{SchemaVersion: 2, Status: "provisioned-not-initialized", Root: root, Genesis: filepath.Join(root, "genesis.json"), GenesisSHA256: hex.EncodeToString(sum[:]), Identity: identity, OwnerDir: filepath.Join(root, "e"), OwnerSocket: filepath.Join(root, "e", "execution-rpc", "execution.sock"), OwnerParallelEVM: true, Transactions: "ethereum-secp256k1", GasPrice: "1", Supply: &supplyRuntimeBinding{Root: source, ManifestSHA256: fmt.Sprintf("%x", sha256.Sum256(manifestRaw)), CorpusID: corpus.CorpusID, GenesisFileSHA256: hex.EncodeToString(sum[:]), BlockIntervalMS: interval}}
	if err := os.Mkdir(out.OwnerDir, 0700); err != nil {
		return err
	}
	if err := writePrivate(out.Genesis, genesisRaw); err != nil {
		return err
	}
	peerIDs := make([]peer.ID, 0, 7)
	for i, validator := range g.Config.HotStuff.Validators {
		dir := filepath.Join(root, fmt.Sprintf("v%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		keydir := filepath.Join(dir, "keystore")
		if err := os.Mkdir(keydir, 0700); err != nil {
			return err
		}
		if err := writePrivate(filepath.Join(keydir, "bls_"+strings.ToLower(types.HexToAddress(validator.Address).Hex())+".key"), privateKeys[i]); err != nil {
			return err
		}
		pkey, _, err := libcrypto.GenerateSecp256k1Key(rand.Reader)
		if err != nil {
			return err
		}
		raw, err := pkey.Raw()
		if err != nil {
			return err
		}
		if err := writePrivate(filepath.Join(dir, "p2p.key"), []byte(hex.EncodeToString(raw)+"\n")); err != nil {
			return err
		}
		id, err := peer.IDFromPrivateKey(pkey)
		if err != nil {
			return err
		}
		peerIDs = append(peerIDs, id)
		out.Validators = append(out.Validators, validatorRecord{Index: i, Address: validator.Address, PeerID: id.String(), DataDir: dir, Config: filepath.Join(dir, "node.json"), APISocket: filepath.Join(dir, "validator-rpc", "validator.sock"), TCPPort: basePort + i})
	}
	if err := writeRuntimeValidatorConfigs(out, peerIDs, basePort, true, interval); err != nil {
		return err
	}
	return writePrivate(filepath.Join(root, "run.json"), out)
}
