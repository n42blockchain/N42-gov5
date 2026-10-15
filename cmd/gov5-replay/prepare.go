package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

const supplyRoot = "/data/blockchain/gov5-performance-5m/supply"

func exclusiveJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func supplyKey(seed []byte, index uint32) (*ecdsa.PrivateKey, error) {
	if len(seed) != 32 {
		return nil, errors.New("32-byte private supply seed required")
	}
	var encoded [8]byte
	binary.LittleEndian.PutUint32(encoded[:4], index)
	for retry := uint32(0); retry < 1024; retry++ {
		binary.LittleEndian.PutUint32(encoded[4:], retry)
		hash := hmac.New(sha256.New, seed)
		hash.Write([]byte(sharedexec.CorpusKeyDerivation))
		hash.Write(encoded[:])
		key, err := crypto.ToECDSA(hash.Sum(nil))
		if err == nil {
			return key, nil
		}
	}
	return nil, errors.New("seed did not derive a valid scalar")
}

func supplyRecipient(index, count uint32) types.Address {
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[:], index%count)
	return types.BytesToAddress(crypto.Keccak256([]byte(sharedexec.CorpusRecipientRule), encoded[:])[12:])
}

func reservedSupplyAddress(address types.Address) bool {
	low := true
	for _, b := range address[:18] {
		if b != 0 {
			low = false
			break
		}
	}
	return low || address == vm.SystemAddress || address == vm.HistoryStorageAddress || address == vm.WithdrawalRequestsAddress || address == vm.ConsolidationRequestsAddress || address == vm.DepositContractAddress || address == params.BeaconRootsAddress
}

func validateSupplyGenesis(g *conf.Genesis) error {
	if g == nil || g.Config == nil || g.Config.Performance == nil || g.Config.HotStuff == nil {
		return errors.New("explicit private performance genesis required")
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if err := g.Config.ValidatePerformanceSelection("private", profile); err != nil {
		return err
	}
	if g.Timestamp != params.PerformanceGenesisTime || !bytes.Equal(g.ExtraData, []byte(params.PerformanceGenesisMarker)) || g.Config.DeferredExecutionTime != nil || g.Config.HotStuff.InteropV4 || g.Config.HotStuff.CommitteePool != nil {
		return errors.New("unsupported supply genesis mode/marker")
	}
	return nil
}

// All senders are checked, including their actual encoded genesis account,
// before any shard is generated. Neither failed state RPC nor absent account
// can silently select nonce zero.
func checkSupplyAccounts(g *conf.Genesis, seed []byte, descriptor sharedexec.CorpusDescriptor) ([]*ecdsa.PrivateKey, error) {
	if g == nil || g.Config == nil || g.Config.HotStuff == nil {
		return nil, errors.New("explicit funded genesis required")
	}
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	keys := make([]*ecdsa.PrivateKey, descriptor.SenderCount)
	seen := make(map[types.Address]bool)
	for i := uint32(0); i < descriptor.SenderCount; i++ {
		key, err := supplyKey(seed, i)
		if err != nil {
			return nil, err
		}
		address := crypto.PubkeyToAddress(key.PublicKey)
		account, ok := g.Alloc[address]
		if !ok || account.Nonce != descriptor.NonceStart || len(account.Code) != 0 || len(account.Storage) != 0 || seen[address] || reservedSupplyAddress(address) {
			return nil, errors.New("all supply sender accounts require unique funded EOA genesis nonces")
		}
		balance, err := uint256.FromHex(account.Balance)
		if err != nil || balance.Cmp(uint256.NewInt(descriptor.NoncesPerSender*21001)) < 0 {
			return nil, errors.New("sender genesis balance cannot fund nonce range")
		}
		keys[i] = key
		seen[address] = true
	}
	for i := uint32(0); i < descriptor.RecipientCount; i++ {
		address := supplyRecipient(i, descriptor.RecipientCount)
		if address == (types.Address{}) || address == g.Coinbase || seen[address] {
			return nil, errors.New("supply recipient collides with sender/beneficiary/recipient")
		}
		if reservedSupplyAddress(address) {
			return nil, errors.New("supply recipient in reserved system address space")
		}
		for _, v := range g.Config.HotStuff.Validators {
			if address == types.HexToAddress(v.Address) {
				return nil, errors.New("supply recipient is validator beneficiary")
			}
		}
		seen[address] = true
	}
	return keys, nil
}

type prepareCost struct {
	Signed    atomic.Uint64
	Verified  atomic.Uint64
	WireBytes atomic.Uint64
}

func prepareShard(ctx context.Context, root string, descriptor sharedexec.CorpusDescriptor, id types.Hash, shard uint16, keys []*ecdsa.PrivateKey, cost *prepareCost) (sharedexec.CorpusShard, error) {
	meta := sharedexec.CorpusShard{Index: shard, File: fmt.Sprintf("shard-%02d.gcf", shard)}
	file, err := os.OpenFile(filepath.Join(root, meta.File), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return meta, err
	}
	defer file.Close()
	hasher := sha256.New()
	buffer := bufio.NewWriterSize(io.MultiWriter(file, hasher), 8<<20)
	// A fresh explicit EIP-155 signer is constructed below; no LatestSigner or
	// chain-global sender hint/cache is used for offline authorization checks.
	signer := transaction.NewLondonSigner(uint256.NewInt(params.PerformanceChainID).ToBig())
	for sequence := uint64(0); ; sequence++ {
		sender, nonce, count, rangeErr := descriptor.FrameRange(shard, sequence)
		if rangeErr != nil {
			break
		}
		if err := ctx.Err(); err != nil {
			return meta, err
		}
		transactions := make([][]byte, count)
		key := keys[sender]
		address := crypto.PubkeyToAddress(key.PublicKey)
		for i := uint16(0); i < count; i++ {
			to := supplyRecipient(uint32((uint64(sender)*descriptor.NoncesPerSender+nonce-descriptor.NonceStart+uint64(i))%uint64(descriptor.RecipientCount)), descriptor.RecipientCount)
			tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{Nonce: nonce + uint64(i), To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
			if err != nil {
				return meta, err
			}
			wire, err := tx.EthEncoded()
			if err != nil {
				return meta, err
			}
			cost.Signed.Add(1)
			decoded, err := transaction.DecodeEthereumTransaction(wire)
			if err != nil {
				return meta, err
			}
			// Direct signer.Sender always performs signature recovery; it does
			// not consult transaction.Sender's process-wide memo/cache.
			recovered, err := signer.Sender(decoded)
			if err != nil || recovered != address || decoded.Type() != transaction.LegacyTxType || !decoded.Protected() || decoded.Nonce() != nonce+uint64(i) || decoded.To() == nil || *decoded.To() != to || decoded.Gas() != 21000 || !decoded.Value().Eq(uint256.NewInt(1)) || !decoded.GasPrice().Eq(uint256.NewInt(1)) || len(decoded.Data()) != 0 {
				return meta, errors.New("offline signed wire verification failed")
			}
			transactions[i] = wire
			cost.Verified.Add(1)
			cost.WireBytes.Add(uint64(len(wire)))
		}
		frame, err := sharedexec.MakeCorpusFrame(sharedexec.CorpusFrameID{CorpusID: id, Shard: shard, Sequence: sequence}, transactions)
		if err != nil {
			return meta, err
		}
		if err := frame.Validate(descriptor); err != nil {
			return meta, err
		}
		if err := sharedexec.WriteCorpusFrame(buffer, frame); err != nil {
			return meta, err
		}
		meta.Transactions += uint64(count)
		meta.Frames++
		meta.Bytes += uint64(sharedexec.CorpusWireHeaderSize + len(frame.Payload))
	}
	if err := buffer.Flush(); err != nil {
		return meta, err
	}
	if err := file.Sync(); err != nil {
		return meta, err
	}
	copy(meta.SHA256[:], hasher.Sum(nil))
	return meta, nil
}

func prepareCorpus(ctx context.Context, root, template, base string, senders uint32, nonces uint64, shards uint16) error {
	if senders == 0 || senders > 12500 || nonces == 0 || nonces > sharedexec.CorpusMaxTransactions || shards == 0 || shards > sharedexec.CorpusMaxShards || uint32(shards) > senders || uint64(senders)*nonces > sharedexec.CorpusMaxTransactions {
		return errors.New("invalid bounded supply preparation shape")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !strings.HasPrefix(root, base+string(filepath.Separator)) {
		return errors.New("fresh dedicated supply child directory required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil || parent != filepath.Dir(root) {
		return errors.New("supply parent must already exist without aliases")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	// Failure intentionally retains partial files and private keys. No cleanup
	// or retry creates a replacement root to hide preparation failures.
	raw, err := os.ReadFile(template)
	if err != nil {
		return err
	}
	var g conf.Genesis
	if err := json.Unmarshal(raw, &g); err != nil {
		return err
	}
	if err := validateSupplyGenesis(&g); err != nil {
		return err
	}
	g.Config.HotStuff.Validators = nil
	for i := 0; i < 7; i++ {
		key, err := bls.RandKey()
		if err != nil {
			return err
		}
		public := key.PublicKey().Marshal()
		address := types.BytesToAddress(crypto.Keccak256(public)[12:])
		g.Config.HotStuff.Validators = append(g.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: address.Hex(), BLSKey: "0x" + hex.EncodeToString(public)})
		dir := filepath.Join(root, fmt.Sprintf("v%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		dir = filepath.Join(dir, "keystore")
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(dir, "bls_"+strings.ToLower(address.Hex())+".key"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.WriteString(hex.EncodeToString(key.Marshal()) + "\n")
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	seedFile, err := os.OpenFile(filepath.Join(root, "sender.seed"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = seedFile.Write(seed)
	closeErr := seedFile.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	g.Alloc = make(conf.GenesisAlloc)
	for i := uint32(0); i < senders; i++ {
		key, err := supplyKey(seed, i)
		if err != nil {
			return err
		}
		g.Alloc[crypto.PubkeyToAddress(key.PublicKey)] = conf.GenesisAccount{Balance: "0x1000000000000000000000000000000"}
	}
	g.BaseFee = uint256.NewInt(1)
	encoded, err := json.Marshal(g)
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		return err
	}
	document["extraData"], _ = json.Marshal("0x" + hex.EncodeToString(g.ExtraData))
	encoded, err = json.Marshal(document)
	if err != nil {
		return err
	}
	genesisPath := filepath.Join(root, "genesis.json")
	file, err := os.OpenFile(genesisPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(encoded)
	closeErr = file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Re-read the exact persisted genesis, then prove every derived key's
	// nonce/funds against it rather than trusting the in-memory writer.
	persisted, err := os.ReadFile(genesisPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(persisted, &g); err != nil {
		return err
	}
	genesis, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		return err
	}
	descriptor := sharedexec.CorpusDescriptor{Version: sharedexec.CorpusVersion, Identity: sharedexec.Identity{Version: 1, Namespace: g.Config.Performance.NetworkNamespace, ChainID: g.Config.ChainID.String(), GenesisHash: genesis.Hash()}, GenesisFileHash: types.Hash(sha256.Sum256(persisted)), Codec: sharedexec.CorpusCodec, Signature: sharedexec.CorpusSignature, KeyDerivation: sharedexec.CorpusKeyDerivation, SeedCommitment: types.Hash(sha256.Sum256(seed)), SenderCount: senders, NoncesPerSender: nonces, RecipientRule: sharedexec.CorpusRecipientRule, RecipientCount: 22857, ShardCount: shards, FrameTransactions: sharedexec.CorpusMaxFrameTxs}
	id, err := descriptor.ID()
	if err != nil {
		return err
	}
	keys, err := checkSupplyAccounts(&g, seed, descriptor)
	if err != nil {
		return err
	}
	started := time.Now()
	var cost prepareCost
	manifest := sharedexec.CorpusManifest{Descriptor: descriptor, CorpusID: id, Shards: make([]sharedexec.CorpusShard, shards)}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsByShard := make([]error, shards)
	var group sync.WaitGroup
	for shard := uint16(0); shard < shards; shard++ {
		group.Add(1)
		go func(shard uint16) {
			defer group.Done()
			manifest.Shards[shard], errorsByShard[shard] = prepareShard(ctx, root, descriptor, id, shard, keys, &cost)
			if errorsByShard[shard] != nil {
				cancel()
			}
		}(shard)
	}
	group.Wait()
	costResult := map[string]any{"scope": "offline-preparation-only", "elapsed_seconds": time.Since(started).Seconds(), "signed_transactions": cost.Signed.Load(), "offline_crypto_verified": cost.Verified.Load(), "raw_wire_bytes": cost.WireBytes.Load(), "online_crypto_verified": 0, "on_chain_committed": 0}
	if err := exclusiveJSON(filepath.Join(root, "prepare-cost.json"), costResult); err != nil {
		return err
	}
	for _, err := range errorsByShard {
		if err != nil {
			return err
		}
	}
	if cost.Signed.Load() != descriptor.TotalTransactions() || cost.Verified.Load() != descriptor.TotalTransactions() {
		return errors.New("offline preparation did not verify entire unique corpus")
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	return exclusiveJSON(filepath.Join(root, "manifest.json"), manifest)
}
