// gov5-perf-provision creates fresh private identities/configs only. It never
// initializes chain databases, launches a process, stops a workload or deletes data.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

const runtimeBase = "/data/blockchain/gov5-performance-5m/runtime"

type validatorRecord struct {
	Index     int    `json:"index"`
	Address   string `json:"address"`
	PeerID    string `json:"peer_id"`
	DataDir   string `json:"data_dir"`
	Config    string `json:"config"`
	APISocket string `json:"api_socket"`
	TCPPort   int    `json:"tcp_port"`
}
type manifest struct {
	SchemaVersion    int                 `json:"schema_version"`
	Status           string              `json:"status"`
	Root             string              `json:"root"`
	Genesis          string              `json:"genesis"`
	GenesisSHA256    string              `json:"genesis_sha256"`
	Identity         sharedexec.Identity `json:"identity"`
	OwnerDir         string              `json:"owner_dir"`
	OwnerSocket      string              `json:"owner_socket"`
	OwnerParallelEVM bool                `json:"owner_parallel_evm"`
	Validators       []validatorRecord   `json:"validators"`
	LoadKeys         string              `json:"load_keys"`
	Transactions     string              `json:"transactions"`
	GasPrice         string              `json:"gas_price"`
}

func main() {
	flags := flag.NewFlagSet("gov5-perf-provision", flag.ExitOnError)
	root := flags.String("run-root", "", "Fresh absolute child of the dedicated performance runtime")
	template := flags.String("template", "", "Absolute empty dedicated performance genesis template")
	senders := flags.Int("senders", 4, "Small functional fixture senders (1..64); not a throughput workload")
	basePort := flags.Int("base-port", 25000, "First of seven reserved loopback P2P TCP ports (25000..29993)")
	smoke := flags.String("smoke-manifest", "", "Explicitly send one small signed batch to the existing private fixture; never a TPS run")
	timeout := flags.Int("timeout-seconds", 30, "Functional transaction finality timeout (5..60 seconds)")
	_ = flags.Parse(os.Args[1:])
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(1)
	}
	if *smoke != "" {
		if *root != "" || *template != "" || *timeout < 5 || *timeout > 60 {
			fmt.Fprintln(os.Stderr, "smoke mode requires only an explicit manifest and timeout 5..60")
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
		defer cancel()
		if err := smokeFixture(ctx, *smoke, runtimeBase); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(filepath.Join(filepath.Dir(*smoke), "smoke-result.json"))
		return
	}
	if err := provision(*root, *template, *senders, *basePort, runtimeBase); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(filepath.Join(*root, "run.json"))
}

func writePrivate(path string, value any) error {
	var raw []byte
	switch data := value.(type) {
	case []byte:
		raw = data
	default:
		var err error
		raw, err = json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

// allowedBase is fixed by main. Tests use a private temporary root, never a
// production namespace. Existing runs/keys are refused instead of overwritten.
func provision(root, template string, senders, basePort int, allowedBase string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !strings.HasPrefix(root, allowedBase+string(filepath.Separator)) || !filepath.IsAbs(template) || senders < 1 || senders > 64 || basePort < 25000 || basePort > 29993 {
		return errors.New("explicit isolated absolute paths, senders 1..64 and base-port 25000..29993 required")
	}
	if len(filepath.Join(root, "v0", "validator-rpc", "validator.sock")) > 107 {
		return errors.New("run root is too long for private Unix sockets")
	}
	raw, err := os.ReadFile(template)
	if err != nil {
		return err
	}
	var g conf.Genesis
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&g); err != nil {
		return err
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if g.Config == nil || g.Config.Performance == nil || g.Config.HotStuff == nil || g.Config.ValidatePerformanceSelection("private", profile) != nil || g.Config.DeferredExecutionTime != nil || g.Config.HotStuff.InteropV4 || g.Config.HotStuff.CommitteePool != nil || len(g.Config.HotStuff.Validators) != 0 || len(g.Alloc) != 0 || g.Timestamp != params.PerformanceGenesisTime || g.Number != 0 || !bytes.Equal(g.ExtraData, []byte(params.PerformanceGenesisMarker)) {
		return errors.New("provisioning requires the empty dedicated independent/local Ethereum performance template")
	}
	parent := filepath.Dir(root)
	ancestor := parent
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || filepath.Dir(ancestor) == ancestor {
			return errors.New("runtime parent ancestry unavailable")
		}
		ancestor = filepath.Dir(ancestor)
	}
	canonicalAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil || canonicalAncestor != ancestor {
		return errors.New("runtime parent ancestry cannot contain symlink aliases")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return errors.New("runtime parent must exist without symlink aliases")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("fresh run root required (existing data is never overwritten): %w", err)
	}
	// A failure leaves a private partial run for review; no cleanup deletes keys.
	out := manifest{SchemaVersion: 1, Status: "provisioned-not-initialized", Root: root, Genesis: filepath.Join(root, "genesis.json"), OwnerDir: filepath.Join(root, "e"), OwnerParallelEVM: true, Transactions: "ethereum-secp256k1", GasPrice: "1"}
	out.OwnerSocket = filepath.Join(out.OwnerDir, "execution-rpc", "execution.sock")
	if err := os.Mkdir(out.OwnerDir, 0700); err != nil {
		return err
	}
	var peerIDs []peer.ID
	for i := 0; i < 7; i++ {
		dir := filepath.Join(root, fmt.Sprintf("v%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		keydir := filepath.Join(dir, "keystore")
		if err := os.Mkdir(keydir, 0700); err != nil {
			return err
		}
		key, err := bls.RandKey()
		if err != nil {
			return err
		}
		public := key.PublicKey().Marshal()
		address := types.BytesToAddress(crypto.Keccak256(public)[12:])
		g.Config.HotStuff.Validators = append(g.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: address.Hex(), BLSKey: "0x" + hex.EncodeToString(public)})
		if err := writePrivate(filepath.Join(keydir, "bls_"+strings.ToLower(address.Hex())+".key"), []byte(hex.EncodeToString(key.Marshal())+"\n")); err != nil {
			return err
		}
		pkey, _, err := libcrypto.GenerateSecp256k1Key(rand.Reader)
		if err != nil {
			return err
		}
		pbytes, err := pkey.Raw()
		if err != nil {
			return err
		}
		if err := writePrivate(filepath.Join(dir, "p2p.key"), []byte(hex.EncodeToString(pbytes)+"\n")); err != nil {
			return err
		}
		id, err := peer.IDFromPrivateKey(pkey)
		if err != nil {
			return err
		}
		peerIDs = append(peerIDs, id)
		out.Validators = append(out.Validators, validatorRecord{Index: i, Address: address.Hex(), PeerID: id.String(), DataDir: dir, Config: filepath.Join(dir, "node.json"), APISocket: filepath.Join(dir, "validator-rpc", "validator.sock"), TCPPort: basePort + i})
	}
	loaddir := filepath.Join(root, "load")
	if err := os.Mkdir(loaddir, 0700); err != nil {
		return err
	}
	out.LoadKeys = filepath.Join(loaddir, "keys.json")
	var loadKeys []string
	g.Alloc = make(conf.GenesisAlloc)
	for i := 0; i < senders; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			return err
		}
		loadKeys = append(loadKeys, hex.EncodeToString(crypto.FromECDSA(key)))
		g.Alloc[crypto.PubkeyToAddress(key.PublicKey)] = conf.GenesisAccount{Balance: "0x1000000000000000000000000000000"}
	}
	if err := writePrivate(out.LoadKeys, loadKeys); err != nil {
		return err
	}
	g.BaseFee = uint256.NewInt(1)
	gbytes, err := json.Marshal(g)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(gbytes, &document); err != nil {
		return err
	}
	// Genesis uses a custom hex byte parser; default []byte Marshal is base64.
	document["extraData"] = "0x" + hex.EncodeToString(g.ExtraData)
	if err := writePrivate(out.Genesis, document); err != nil {
		return err
	}
	gbytes, err = os.ReadFile(out.Genesis)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(gbytes, &g); err != nil {
		return err
	}
	gb, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(gbytes)
	out.GenesisSHA256 = hex.EncodeToString(sum[:])
	out.Identity = sharedexec.Identity{Version: 1, Namespace: g.Config.Performance.NetworkNamespace, ChainID: g.Config.ChainID.String(), GenesisHash: gb.Hash()}
	for _, v := range out.Validators {
		var peers []string
		for j, id := range peerIDs {
			if j != v.Index {
				peers = append(peers, fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", basePort+j, id))
			}
		}
		// JSON is valid YAML. The existing loader uses YAML tags, so untagged
		// MinerConfig fields are deliberately lower-case, not Go's JSON names.
		config := map[string]any{
			"node":  map[string]any{"data_dir": v.DataDir, "chain": "private", "profile": "n42", "performance_genesis": out.Genesis, "performance_role": "validator", "performance_execution_socket": out.OwnerSocket, "miner": true, "http": false, "ws": false, "auth_rpc": false, "ipc_path": ""},
			"chain": nil,
			"miner": map[string]any{"etherbase": v.Address, "blockintervalms": uint64(2000)},
			"p2p":   map[string]any{"private_key": filepath.Join(v.DataDir, "p2p.key"), "data_dir": v.DataDir, "local_ip": "127.0.0.1", "host_address": "127.0.0.1", "tcp_port": v.TCPPort, "udp_port": v.TCPPort, "max_peers": 16, "static_peers": peers, "no_discovery": true, "quic_enabled": false, "bootstrap_node_addr": []string{}, "discv5_bootstrap_addr": []string{}, "relay_node_addr": "", "enable_upnp": false},
		}
		if err := writePrivate(v.Config, config); err != nil {
			return err
		}
	}
	return writePrivate(filepath.Join(root, "run.json"), out)
}
