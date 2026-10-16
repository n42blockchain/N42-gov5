package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

const strictRuntimeBase = "/data/blockchain/gov5-performance-5m/runtime"

type strictRuntime struct {
	SchemaVersion    int                 `json:"schema_version"`
	Status           string              `json:"status"`
	Root             string              `json:"root"`
	Genesis          string              `json:"genesis"`
	GenesisSHA256    string              `json:"genesis_sha256"`
	Identity         sharedexec.Identity `json:"identity"`
	OwnerDir         string              `json:"owner_dir"`
	OwnerSocket      string              `json:"owner_socket"`
	OwnerParallelEVM bool                `json:"owner_parallel_evm"`
	Validators       []struct {
		Index     int    `json:"index"`
		Address   string `json:"address"`
		PeerID    string `json:"peer_id"`
		DataDir   string `json:"data_dir"`
		Config    string `json:"config"`
		APISocket string `json:"api_socket"`
		TCPPort   int    `json:"tcp_port"`
	} `json:"validators"`
	Transactions string `json:"transactions"`
	GasPrice     string `json:"gas_price"`
	Supply       struct {
		Root              string     `json:"root"`
		ManifestSHA256    string     `json:"manifest_sha256"`
		CorpusID          types.Hash `json:"corpus_id"`
		GenesisFileSHA256 string     `json:"genesis_file_sha256"`
		BlockIntervalMS   uint64     `json:"block_interval_ms"`
	} `json:"supply"`
}

func strictPrivatePath(path string, directory, socket bool) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || resolved != path {
		return errors.New("strict runtime path must exist without aliases")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != mode || (directory && !info.IsDir()) || (socket && info.Mode()&os.ModeSocket == 0) || (!directory && !socket && !info.Mode().IsRegular()) {
		return errors.New("strict runtime path must have private owned type/mode")
	}
	return nil
}
func strictMetadata(path string, limit int64) ([]byte, error) {
	if err := strictPrivatePath(path, false, false); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("strict metadata too large/not regular")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("strict metadata grew beyond limit")
	}
	return raw, nil
}
func loadStrictRuntime(path, source, base string, m supplyManifest) (strictRuntime, error) {
	var out strictRuntime
	root := filepath.Dir(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path != filepath.Join(root, "run.json") || !strings.HasPrefix(root, base+string(filepath.Separator)) {
		return out, errors.New("explicit dedicated schema-2 runtime required")
	}
	if err := strictPrivatePath(root, true, false); err != nil {
		return out, err
	}
	raw, err := strictMetadata(path, 2<<20)
	if err != nil {
		return out, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return out, errors.New("strict runtime requires one JSON object")
	}
	if out.SchemaVersion != 2 || out.Status != "provisioned-not-initialized" || out.Root != root || out.Genesis != filepath.Join(root, "genesis.json") || out.OwnerDir != filepath.Join(root, "e") || out.OwnerSocket != filepath.Join(root, "e", "execution-rpc", "execution.sock") || out.Identity != m.Descriptor.Identity || !out.OwnerParallelEVM || out.Transactions != "ethereum-secp256k1" || out.GasPrice != "1" || len(out.Validators) != 7 || m.Descriptor.NonceStart != 0 {
		return out, errors.New("strict runtime topology/profile/identity mismatch")
	}
	if out.GenesisSHA256 != strings.TrimPrefix(m.Descriptor.GenesisFileHash.Hex(), "0x") || out.Supply.Root != source || "0x"+out.Supply.ManifestSHA256 != m.Digest.Hex() || out.Supply.CorpusID != m.CorpusID || out.Supply.GenesisFileSHA256 != out.GenesisSHA256 || out.Supply.BlockIntervalMS > 60000 {
		return out, errors.New("strict source/runtime binding mismatch")
	}
	genesisRaw, err := strictMetadata(out.Genesis, 16<<20)
	if err != nil {
		return out, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(genesisRaw)) != out.GenesisSHA256 {
		return out, errors.New("strict runtime genesis bytes differ")
	}
	addresses := make(map[string]bool)
	peers := make(map[string]bool)
	for i, v := range out.Validators {
		dir := filepath.Join(root, fmt.Sprintf("v%d", i))
		if v.Index != i || v.DataDir != dir || v.Config != filepath.Join(dir, "node.json") || v.APISocket != filepath.Join(dir, "validator-rpc", "validator.sock") || v.TCPPort < 25000 || v.TCPPort > 29999 || addresses[strings.ToLower(v.Address)] || peers[v.PeerID] || v.Address == "" || v.PeerID == "" {
			return out, errors.New("strict validator topology is not seven independent identities")
		}
		addresses[strings.ToLower(v.Address)] = true
		peers[v.PeerID] = true
	}
	return out, nil
}
func strictQueueEmpty(s sharedexec.StrictIngressStatus) bool {
	return !s.Reconciling && s.PendingTransactions == 0 && s.ReservedTransactions == 0 && s.RetainedEncodedBytes == 0 && s.ReservedEncodedBytes == 0
}
func strictValidatorBinding(s sharedvalidator.StatusResult, epoch [16]byte, m supplyManifest) error {
	b := s.OwnerQueue
	if s.Identity != m.Descriptor.Identity || b == nil || b.Epoch != epoch || b.ManifestHash != m.Digest || b.MaxTransactions != sharedvalidator.DefaultOwnerQueueTransactions || b.MaxEncodedBytes != sharedexec.MaxQueueBuildEncodedBytes {
		return errors.New("strict validator owner binding mismatch")
	}
	return nil
}
