package sharedexec

// This opt-in diagnostic uses a fresh chain, never a retained performance
// datadir or source corpus. It measures one block, not sustained throughput.
import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"
	mdbxlib "github.com/erigontech/mdbx-go/mdbx"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

const unixProfileSenders = 6000
const unixProfileNonces = 28
const unixProfileTransactions = 163000

type unixProfileInput struct {
	genesis        conf.Genesis
	keys           []blscommon.SecretKey
	senders        []types.Address
	hashes         map[types.Address][]types.Hash
	frames         []CorpusFrame
	manifest       []byte
	genesisSHA     types.Hash
	fixtureSeedSHA types.Hash
}

type unixProfileStage struct {
	Name              string `json:"name"`
	GOMAXPROCSBefore  int    `json:"gomaxprocs_before"`
	GOMAXPROCSDuring  int    `json:"gomaxprocs_during"`
	GOMAXPROCSAfter   int    `json:"gomaxprocs_after"`
	WallNS            int64  `json:"wall_ns"`
	AllocatedBytes    uint64 `json:"allocated_bytes"`
	Mallocs           uint64 `json:"mallocs"`
	ProcessPeakRSSKiB uint64 `json:"process_peak_rss_kib_after_stage"`
	CPUProfile        string `json:"cpu_profile"`
	AllocBefore       string `json:"alloc_before"`
	AllocAfter        string `json:"alloc_after"`
}

type unixProfileCertifiedReadCounts struct {
	StartedFlights uint64 `json:"started_flights"`
	JoinedCallers  uint64 `json:"joined_callers"`
}

type unixProfileResult struct {
	Kind                           string                         `json:"kind"`
	Passed                         bool                           `json:"passed"`
	TPSMeasured                    bool                           `json:"tps_measured"`
	ReuseOwnBuild                  bool                           `json:"reuse_own_build"`
	GOMAXPROCS                     int                            `json:"gomaxprocs"`
	FixtureSeedSHA                 types.Hash                     `json:"fixture_seed_sha256"`
	GenesisSHA                     types.Hash                     `json:"genesis_sha256"`
	ManifestSHA                    types.Hash                     `json:"manifest_sha256"`
	Transactions                   int                            `json:"transactions"`
	Senders                        int                            `json:"senders"`
	Preloaded                      int                            `json:"preloaded_transactions"`
	Pending                        int                            `json:"pending_transactions"`
	BlockHash                      types.Hash                     `json:"block_hash"`
	Root                           types.Hash                     `json:"root"`
	ReceiptHash                    types.Hash                     `json:"receipt_hash"`
	GasUsed                        uint64                         `json:"gas_used"`
	AccountVectorSHA               types.Hash                     `json:"account_vector_sha256"`
	OwnBuildReuses                 uint64                         `json:"own_build_reuses"`
	ExecutionAttemptsBefore        uint64                         `json:"execution_attempts_before"`
	ExecutionAttemptsAfterBuild    uint64                         `json:"execution_attempts_after_build"`
	ExecutionAttemptsAfterImport   uint64                         `json:"execution_attempts_after_import"`
	ExecutionAttemptsDelta         uint64                         `json:"execution_attempts_delta"`
	PreparedPresent                bool                           `json:"prepared_present_after_build"`
	PreparedRetainedBytes          int                            `json:"prepared_retained_bytes_after_build"`
	CertifiedReadsBeforeDuplicates unixProfileCertifiedReadCounts `json:"certified_reads_before_duplicates"`
	CertifiedReadsAfterDuplicates  unixProfileCertifiedReadCounts `json:"certified_reads_after_duplicates"`
	DuplicateWallNS                [6]int64                       `json:"duplicate_wall_ns"`
	Stages                         []unixProfileStage             `json:"stages"`
	ProcessPeakRSSKiB              uint64                         `json:"process_peak_rss_kib"`
	MDBXPolicy                     string                         `json:"mdbx_policy"`
	MDBXFlags                      uint                           `json:"mdbx_actual_flags"`
}

func unixProfileWrite(t *testing.T, path string, write func(*os.File) error) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := write(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
}

// CPU samples cover the RPC and all server work in this process. Wall time is
// disjoint at the client boundary. Allocation profiles must be subtracted with
// pprof -base; their cumulative setup allocations are not phase allocations.
// GC and profiler startup/flush run outside the recorded operation wall time.
func unixProfileMeasure(t *testing.T, dir, name string, out *unixProfileResult, operation func() error) {
	t.Helper()
	stage := unixProfileStage{Name: name, CPUProfile: name + ".cpu.pprof", AllocBefore: name + ".alloc-before.pprof", AllocAfter: name + ".alloc-after.pprof"}
	stage.GOMAXPROCSBefore = runtime.GOMAXPROCS(out.GOMAXPROCS)
	defer runtime.GOMAXPROCS(stage.GOMAXPROCSBefore)
	if stage.GOMAXPROCSBefore != 2 {
		t.Fatal("profile setup/validation must use GOMAXPROCS=2")
	}
	stage.GOMAXPROCSDuring = runtime.GOMAXPROCS(0)
	runtime.GC()
	unixProfileWrite(t, filepath.Join(dir, stage.AllocBefore), func(f *os.File) error { return pprof.Lookup("allocs").WriteTo(f, 0) })
	var before, after runtime.MemStats
	unixProfileWrite(t, filepath.Join(dir, stage.CPUProfile), func(f *os.File) error {
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
		runtime.ReadMemStats(&before)
		start := time.Now()
		err := operation()
		stage.WallNS = time.Since(start).Nanoseconds()
		runtime.ReadMemStats(&after)
		return err
	})
	stage.AllocatedBytes, stage.Mallocs = after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs
	stage.ProcessPeakRSSKiB = unixProfilePeakRSS(t)
	runtime.GC()
	unixProfileWrite(t, filepath.Join(dir, stage.AllocAfter), func(f *os.File) error { return pprof.Lookup("allocs").WriteTo(f, 0) })
	runtime.GOMAXPROCS(stage.GOMAXPROCSBefore)
	stage.GOMAXPROCSAfter = runtime.GOMAXPROCS(0)
	out.Stages = append(out.Stages, stage)
	t.Logf("Unix profile stage %s wall_ns=%d alloc_bytes=%d mallocs=%d", name, stage.WallNS, stage.AllocatedBytes, stage.Mallocs)
}

func unixProfileMakeInput(t *testing.T) unixProfileInput {
	t.Helper()
	var in unixProfileInput
	seed := os.Getenv("GOV5_UNIX_PROFILE_SEED")
	if seed != "" {
		in.fixtureSeedSHA = types.Hash(sha256.Sum256([]byte(seed)))
	}
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &in.genesis); err != nil {
		t.Fatal(err)
	}
	in.genesis.Alloc = make(conf.GenesisAlloc)
	in.genesis.BaseFee = uint256.NewInt(1)
	reserved := make(map[types.Address]bool)
	for i := uint32(0); i < 22857; i++ {
		address := CorpusRecipient(i, 22857)
		if reserved[address] {
			t.Fatal("profile recipient collision")
		}
		reserved[address] = true
		in.genesis.Alloc[address] = conf.GenesisAccount{Balance: "0x1"}
	}
	for i := 0; i < 7; i++ {
		address := types.Address{19: byte(i + 1)}
		if reserved[address] {
			t.Fatal("profile validator/recipient collision")
		}
		reserved[address] = true
	}
	in.hashes = make(map[types.Address][]types.Hash)
	private := make([]*ecdsa.PrivateKey, unixProfileSenders)
	for i := range private {
		if seed == "" {
			private[i], err = crypto.GenerateKey()
		} else {
			// Public deterministic test material makes independent A/B/A process
			// inputs identical. These keys must never leave this fresh fixture.
			key := sha256.Sum256([]byte(fmt.Sprintf("gov5/unix-profile-test-only/ecdsa/%s/%d", seed, i)))
			private[i], err = crypto.ToECDSA(key[:])
		}
		if err != nil {
			t.Fatal(err)
		}
		address := crypto.PubkeyToAddress(private[i].PublicKey)
		if reserved[address] {
			t.Fatal("profile sender collision")
		}
		reserved[address] = true
		in.senders = append(in.senders, address)
		in.genesis.Alloc[address] = conf.GenesisAccount{Balance: "0x1000000000000000000000000000000"}
	}
	for i := 0; i < 7; i++ {
		var key blscommon.SecretKey
		if seed == "" {
			key, err = bls.RandKey()
		} else {
			ikm := sha256.Sum256([]byte(fmt.Sprintf("gov5/unix-profile-test-only/bls/%s/%d", seed, i)))
			key, err = bls.SecretKeyFromRandom32Byte(ikm)
		}
		if err != nil {
			t.Fatal(err)
		}
		in.keys = append(in.keys, key)
		address := types.Address{19: byte(i + 1)}
		in.genesis.Config.HotStuff.Validators = append(in.genesis.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: address.Hex(), BLSKey: fmt.Sprintf("0x%x", key.PublicKey().Marshal())})
	}
	genesisRaw, err := json.Marshal(&in.genesis)
	if err != nil {
		t.Fatal(err)
	}
	in.genesisSHA = types.Hash(sha256.Sum256(genesisRaw))
	gb, _, err := (&internal.GenesisBlock{GenesisConfig: &in.genesis}).ToBlock()
	if err != nil {
		t.Fatal(err)
	}
	d := corpusDescriptorForTest()
	d.Identity = Identity{Version: 1, Namespace: params.PerformanceNamespace, ChainID: in.genesis.Config.ChainID.String(), GenesisHash: gb.Hash()}
	d.GenesisFileHash, d.SenderCount, d.NonceStart, d.NoncesPerSender = in.genesisSHA, unixProfileSenders, 0, unixProfileNonces
	d.ShardCount, d.FrameTransactions, d.RecipientCount = 64, unixProfileNonces, 22857
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	m := CorpusManifest{Descriptor: d, CorpusID: id}
	for shard := uint16(0); shard < d.ShardCount; shard++ {
		var encoded bytes.Buffer
		var sequence uint64
		for sender := int(shard); sender < unixProfileSenders; sender += int(d.ShardCount) {
			raws := make([][]byte, unixProfileNonces)
			for nonce := uint64(0); nonce < unixProfileNonces; nonce++ {
				to := CorpusRecipient(uint32(uint64(sender)*unixProfileNonces+nonce), d.RecipientCount)
				txn, err := transaction.SignNewTx(private[sender], transaction.NewEIP155Signer(in.genesis.Config.ChainID), &transaction.LegacyTx{Nonce: nonce, To: &to, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1)})
				if err != nil {
					t.Fatal(err)
				}
				raws[nonce], err = txn.EthEncoded()
				if err != nil {
					t.Fatal(err)
				}
				in.hashes[in.senders[sender]] = append(in.hashes[in.senders[sender]], txn.Hash())
			}
			frame, err := MakeCorpusFrame(CorpusFrameID{CorpusID: id, Shard: shard, Sequence: sequence}, raws)
			if err != nil {
				t.Fatal(err)
			}
			in.frames = append(in.frames, frame)
			if err := WriteCorpusFrame(&encoded, frame); err != nil {
				t.Fatal(err)
			}
			sequence++
		}
		m.Shards = append(m.Shards, CorpusShard{Index: shard, File: fmt.Sprintf("shard-%02d.gcf", shard), Transactions: sequence * unixProfileNonces, Frames: sequence, Bytes: uint64(encoded.Len()), SHA256: types.Hash(sha256.Sum256(encoded.Bytes()))})
	}
	in.manifest, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCorpusManifest(in.manifest); err != nil {
		t.Fatal(err)
	}
	return in
}

func unixProfileChain(t *testing.T, dir string, in unixProfileInput) (*internal.BlockChain, *hotstuff.HotStuff, *block.Header) {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	// Durable MDBX, unlike memdb's UtterlyNoSync. Only geometry is reduced for
	// this independent single-block diagnostic; no retained DB is opened.
	db, err := mdbx.NewMDBX(log.New()).Path(filepath.Join(dir, "chaindata")).Label(kv.ChainDB).WriteMergeThreshold(4 * 8192).MapSize(8 * datasize.GB).GrowthStep(256 * datasize.MB).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	flags, err := db.(*mdbx.MdbxKV).Env().Flags()
	if err != nil {
		t.Fatal(err)
	}
	if flags&uint(mdbxlib.SafeNoSync|mdbxlib.NoMetaSync|mdbxlib.WriteMap) != 0 || flags&uint(mdbxlib.NoReadahead) == 0 {
		t.Fatalf("unexpected actual MDBX flags %#x", flags)
	}
	var genesis *block.Block
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		var err error
		genesis, _, err = (&internal.GenesisBlock{GenesisConfig: &in.genesis}).Write(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	public := hotstuff.New(in.genesis.Config.HotStuff, in.genesis.Config)
	public.SetRewardFunc(apos.DoReward)
	if err := public.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = public.Close() })
	ibc, err := internal.NewBlockChain(context.Background(), genesis, public, db, nil, in.genesis.Config)
	if err != nil {
		t.Fatal(err)
	}
	chain := ibc.(*internal.BlockChain)
	t.Cleanup(func() { _ = chain.Close() })
	root := commitment.NewQMDBRootComputer()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		return loadExecutionFixtureRoot(tx, root, &in.genesis, func(d fixtureQMDBDiagnostic) {
			raw, _ := json.Marshal(d)
			t.Logf("fixture QMDB load diagnostic: %s", raw)
		})
	}); err != nil {
		t.Fatal(err)
	}
	root.SetCold(nil)
	root.EnableUndoRecording()
	chain.SetQMDBRootComputer(root)
	chain.SetParallelEVM(true)
	leader := hotstuff.New(in.genesis.Config.HotStuff, in.genesis.Config)
	set := public.Engine().ValidatorSetForView(1)
	index := hotstuff.LeaderForView(1, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	leader.Authorize(address, in.keys[int(index)])
	if err := leader.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leader.Close() })
	header := &block.Header{ParentHash: genesis.Hash(), Number: uint256.NewInt(1), GasLimit: in.genesis.GasLimit, Time: in.genesis.Timestamp + 1, Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1)}
	if err := leader.Prepare(chain, header); err != nil {
		t.Fatal(err)
	}
	header.TxHash = block.TxRootAt(nil, header.Time)
	header.ResetHashCache()
	return chain, leader, header
}

func unixProfilePeakRSS(t *testing.T) uint64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "VmHWM:" && fields[2] == "kB" {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
	}
	t.Fatal("process peak RSS is unavailable")
	return 0
}

func unixProfileSocket(t *testing.T, server *Server) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "execute.sock")
	httpServer, listener, err := server.ListenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpServer.Handler
	// Newly spawned workers inherit their RPC label. Pre-existing workers
	// may be unlabelled; inspect their call stacks separately from commit.
	httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pprof.Do(r.Context(), pprof.Labels("gov5_rpc", r.URL.Path), func(ctx context.Context) {
			handler.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close(); server.Close() })
	return socket
}

func TestUnix163kOperationProfile(t *testing.T) {
	dir := os.Getenv("GOV5_UNIX_PROFILE_DIR")
	if dir == "" {
		t.Skip("opt-in independent source diagnostic; no TPS measurement")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("profile output must be an absolute fresh directory")
	}
	for _, name := range []string{"NO_SYNC", "WRITE_MAP", "MDBX_READAHEAD", "MERGE_THRESHOLD", "MDBX_DIRTY_SPACE_MB", "N42_MDBX_SYNC", "N42_MDBX_MAPSIZE_GB", "N42_MDBX_DIRTY_GB"} {
		if _, present := os.LookupEnv(name); present {
			t.Fatalf("profile refuses MDBX environment override %s", name)
		}
	}
	profileCores := 36
	if raw := os.Getenv("GOV5_UNIX_PROFILE_GOMAXPROCS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || (value != 2 && value != 36) {
			t.Fatal("profile GOMAXPROCS must be 2 or 36")
		}
		profileCores = value
	}
	mode := os.Getenv("GOV5_UNIX_PROFILE_MODE")
	if mode != "" && mode != "strict-import" && mode != "reuse-own-build" {
		t.Fatal("unknown profile mode")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	oldParallel := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(oldParallel)
	in := unixProfileMakeInput(t) // all key generation/signing is outside profiles
	var results []unixProfileResult
	for _, reuse := range []bool{false, true} {
		name := "strict-import"
		if reuse {
			name = "reuse-own-build"
		}
		if mode != "" && mode != name {
			continue
		}
		t.Run(name, func(t *testing.T) {
			results = append(results, unixProfileMode(t, filepath.Join(dir, name), in, reuse, profileCores))
		})
		if t.Failed() {
			return
		}
	}
	if len(results) == 2 && (results[0].BlockHash != results[1].BlockHash || results[0].GenesisSHA != results[1].GenesisSHA || results[0].ManifestSHA != results[1].ManifestSHA || results[0].Root != results[1].Root || results[0].ReceiptHash != results[1].ReceiptHash || results[0].GasUsed != results[1].GasUsed || results[0].AccountVectorSHA != results[1].AccountVectorSHA) {
		t.Fatal("strict and reuse modes produced different actual execution state")
	}
	unixProfileWrite(t, filepath.Join(dir, "profile-result.json"), func(f *os.File) error { return json.NewEncoder(f).Encode(results) })
}

func unixProfileMode(t *testing.T, dir string, in unixProfileInput, reuse bool, profileCores int) unixProfileResult {
	t.Helper()
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	chain, leader, header := unixProfileChain(t, dir, in)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if reuse {
		if err := server.EnableOwnBuildReuse(); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.EnableStrictIngress(StrictIngressOptions{Manifest: in.manifest, MaxTransactions: 2000000, MaxEncodedBytes: 512 << 20}); err != nil {
		t.Fatal(err)
	}
	status, err := server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range in.frames {
		ack, err := server.AdmitStrictFrame(context.Background(), server.Identity(), status.Epoch, frame)
		if err != nil || ack.NewlyAdmitted != unixProfileNonces {
			t.Fatalf("profile preload: %+v %v", ack, err)
		}
	}
	socket := unixProfileSocket(t, server)
	client := NewClient(socket, server.Identity())
	defer client.Close()
	prepared, err := block.NewBlock(header, nil).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	request := QueueBuildRequest{Identity: server.Identity(), Epoch: status.Epoch, PreparedHash: crypto.Keccak256Hash(prepared), PreparedRLP: prepared, MaxTransactions: unixProfileTransactions, MaxEncodedBytes: 32 << 20}
	out := unixProfileResult{Kind: "gov5-independent-unix-single-block-profile-v1", ReuseOwnBuild: reuse, GOMAXPROCS: profileCores, FixtureSeedSHA: in.fixtureSeedSHA, GenesisSHA: in.genesisSHA, ManifestSHA: types.Hash(sha256.Sum256(in.manifest)), Senders: unixProfileSenders, Preloaded: unixProfileSenders * unixProfileNonces, MDBXPolicy: "durable/no-readahead; merge=32768; map=8GiB; growth=256MiB; independent fresh DB; no env overrides"}
	out.MDBXFlags, err = chain.DB().(*mdbx.MdbxKV).Env().Flags()
	if err != nil {
		t.Fatal(err)
	}
	var built QueueBuildResult
	out.ExecutionAttemptsBefore = chain.PerformanceExecutionAttempts()
	unixProfileMeasure(t, dir, "build-from-queue-rpc", &out, func() error {
		var err error
		built, err = client.BuildFromQueue(context.Background(), request)
		return err
	})
	out.ExecutionAttemptsAfterBuild = chain.PerformanceExecutionAttempts()
	server.execute.RLock()
	out.PreparedPresent = server.prepared != nil
	if server.prepared != nil {
		out.PreparedRetainedBytes = server.prepared.RetainedBytes()
	}
	server.execute.RUnlock()
	t.Logf("profile build reuse=%t prepared=%t retained_bytes=%d execution_attempts=%d", reuse, out.PreparedPresent, out.PreparedRetainedBytes, out.ExecutionAttemptsAfterBuild-out.ExecutionAttemptsBefore)
	unixProfileWrite(t, filepath.Join(dir, "prepared-after-build.json"), func(f *os.File) error {
		return json.NewEncoder(f).Encode(struct {
			Reuse             bool
			Present           bool
			RetainedBytes     int
			ExecutionAttempts uint64
		}{reuse, out.PreparedPresent, out.PreparedRetainedBytes, out.ExecutionAttemptsAfterBuild - out.ExecutionAttemptsBefore})
	})
	if out.ExecutionAttemptsAfterBuild-out.ExecutionAttemptsBefore != 1 || out.PreparedPresent != reuse || (reuse && (out.PreparedRetainedBytes < 1 || out.PreparedRetainedBytes > internal.MaxPreparedCandidateBytes)) {
		t.Fatal("profile did not retain the requested build execution path; preserve miss without raising the budget")
	}
	var unsigned block.Block
	unixProfileMeasure(t, dir, "unsigned-unmarshal", &out, func() error { return unsigned.Unmarshal(built.BlockRLP) })
	if len(unsigned.Transactions()) != unixProfileTransactions {
		t.Fatalf("selection returned %d", len(unsigned.Transactions()))
	}
	// Independently derive the expected round-robin body before inspecting the
	// actual selected ledger: 27 full address-sorted rounds, then 1000 senders.
	sorted := append([]types.Address(nil), in.senders...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i][:], sorted[j][:]) < 0 })
	for i, txn := range unsigned.Transactions() {
		address := sorted[i%unixProfileSenders]
		nonce := i / unixProfileSenders
		if txn.Hash() != in.hashes[address][nonce] || txn.Nonce() != uint64(nonce) {
			t.Fatalf("selected body differs from predetermined round-robin at %d", i)
		}
	}
	var signed *block.Block
	unixProfileMeasure(t, dir, "seal", &out, func() error {
		sealed := make(chan block.IBlock, 1)
		if err := leader.Seal(chain, &unsigned, sealed, make(chan struct{})); err != nil {
			return err
		}
		signed = (<-sealed).(*block.Block)
		return nil
	})
	var sealedRaw []byte
	unixProfileMeasure(t, dir, "sealed-marshal", &out, func() error { var err error; sealedRaw, err = signed.Marshal(); return err })
	var imported ImportResult
	unixProfileMeasure(t, dir, "import-rpc", &out, func() error {
		var err error
		imported, err = client.Import(context.Background(), ImportRequest{Identity: server.Identity(), BlockHash: signed.Hash(), BlockRLP: sealedRaw})
		return err
	})
	out.ExecutionAttemptsAfterImport = chain.PerformanceExecutionAttempts()
	out.ExecutionAttemptsDelta = out.ExecutionAttemptsAfterImport - out.ExecutionAttemptsBefore
	if out.ExecutionAttemptsDelta != map[bool]uint64{false: 2, true: 1}[reuse] {
		t.Fatalf("profile actual execution attempts %d", out.ExecutionAttemptsDelta)
	}
	if !imported.Durable || imported.Result != built.Result {
		t.Fatalf("profile durable import differs: %+v", imported)
	}
	unixProfileMeasure(t, dir, "applied-child-rpc", &out, func() error {
		child, err := client.AppliedChild(context.Background(), AppliedChildRequest{Identity: server.Identity(), ParentHash: signed.ParentHash(), Number: 1, View: 2})
		if err != nil {
			return err
		}
		if child.Executed == nil || child.Executed.BlockHash != signed.Hash() || !bytes.Equal(child.BlockRLP, sealedRaw) {
			return fmt.Errorf("profile applied-child differs")
		}
		return nil
	})
	commit := CommitRequest{Identity: server.Identity(), BlockHash: signed.Hash(), ParentHash: signed.ParentHash(), Number: 1}
	proof := commitProof(in.keys, signed.Hash(), 5, false)
	unixProfileMeasure(t, dir, "fresh-commit-rpc", &out, func() error {
		result, err := client.CommitCertified(context.Background(), commit, proof)
		if err == nil && (!result.Committed || result.HeadHash != signed.Hash()) {
			return fmt.Errorf("profile commit result differs")
		}
		return err
	})
	out.CertifiedReadsBeforeDuplicates.StartedFlights, out.CertifiedReadsBeforeDuplicates.JoinedCallers = server.CertifiedDuplicateReadCounts()
	unixProfileMeasure(t, dir, "six-duplicate-commit-rpcs", &out, func() error {
		var wg, ready sync.WaitGroup
		startTogether := make(chan struct{})
		errors := make([]error, 6)
		ready.Add(len(errors))
		for i := range errors {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				c := NewClient(socket, server.Identity())
				defer c.Close()
				qc := proof.Clone()
				ready.Done()
				<-startTogether
				start := time.Now()
				result, err := c.CommitCertified(context.Background(), commit, &qc)
				out.DuplicateWallNS[i] = time.Since(start).Nanoseconds()
				if err == nil && (!result.Committed || result.HeadHash != signed.Hash()) {
					err = fmt.Errorf("duplicate %d result differs", i)
				}
				errors[i] = err
			}(i)
		}
		ready.Wait()
		close(startTogether)
		wg.Wait()
		for _, err := range errors {
			if err != nil {
				return err
			}
		}
		return nil
	})
	out.CertifiedReadsAfterDuplicates.StartedFlights, out.CertifiedReadsAfterDuplicates.JoinedCallers = server.CertifiedDuplicateReadCounts()
	beforeRejected, err := server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CommitCertified(context.Background(), commit, commitProof(in.keys, signed.Hash(), 4, false)); err == nil {
		t.Fatal("profile accepted four public votes")
	}
	afterRejected, err := server.StrictIngressStatus()
	if err != nil || beforeRejected != afterRejected {
		t.Fatal("rejected canonical QC changed queue/status")
	}
	// Validation is outside the profiles. Derive the expected ledger from the
	// actual selected signed body, then read all affected accounts/root via Unix.
	out.BlockHash, out.Root, out.ReceiptHash, out.GasUsed = signed.Hash(), built.Result.Root, built.Result.ReceiptHash, built.Result.GasUsed
	out.Transactions = len(signed.Transactions())
	out.OwnBuildReuses = server.OwnBuildReuses()
	if out.GasUsed != unixProfileTransactions*21000 || out.OwnBuildReuses != map[bool]uint64{false: 0, true: 1}[reuse] {
		t.Fatal("profile gas or reuse path mismatch")
	}
	status, err = server.StrictIngressStatus()
	if err != nil || status.PendingTransactions != 5000 || status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 || status.Reconciling || status.CanonicalHash != signed.Hash() || status.CanonicalNumber != 1 || status.CanonicalGeneration != 1 || status.CryptoVerified != 168000 {
		t.Fatalf("profile queue mismatch: %+v %v", status, err)
	}
	out.Pending = status.PendingTransactions
	out.AccountVectorSHA = unixProfileValidate(t, client, chain, in, signed, status)
	out.Passed = true
	out.ProcessPeakRSSKiB = unixProfilePeakRSS(t)
	unixProfileWrite(t, filepath.Join(dir, "result.json"), func(f *os.File) error { return json.NewEncoder(f).Encode(out) })
	return out
}

func unixProfileValidate(t *testing.T, client *Client, chain *internal.BlockChain, in unixProfileInput, b *block.Block, status StrictIngressStatus) types.Hash {
	t.Helper()
	type ledger struct {
		nonce   uint64
		balance uint256.Int
	}
	expected := make(map[types.Address]*ledger)
	for i := uint32(0); i < 22857; i++ {
		address := CorpusRecipient(i, 22857)
		expected[address] = &ledger{balance: *uint256.NewInt(1)}
	}
	for _, address := range in.senders {
		balance, err := uint256.FromHex(in.genesis.Alloc[address].Balance)
		if err != nil {
			t.Fatal(err)
		}
		expected[address] = &ledger{balance: *balance}
	}
	signer := transaction.NewEIP155Signer(chain.Config().ChainID)
	for _, txn := range b.Transactions() {
		from, err := signer.Sender(txn)
		if err != nil {
			t.Fatal(err)
		}
		a := expected[from]
		if a == nil || txn.Nonce() != a.nonce {
			t.Fatal("actual selected sender/nonce discontinuity")
		}
		a.nonce++
		a.balance.SubUint64(&a.balance, 21001)
		to := *txn.To()
		if expected[to] == nil {
			expected[to] = &ledger{}
		}
		expected[to].balance.AddUint64(&expected[to].balance, 1)
	}
	addresses := make([]types.Address, 0, len(expected))
	for address := range expected {
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })
	var vector []CanonicalAccount
	for start := 0; start < len(addresses); start += 1000 {
		end := min(start+1000, len(addresses))
		got, err := client.CanonicalAccounts(context.Background(), CanonicalAccountsRequest{Identity: client.identity, Epoch: status.Epoch, ManifestHash: status.ManifestHash, CanonicalHash: b.Hash(), CanonicalNumber: 1, Addresses: addresses[start:end], IncludeRoots: true})
		if err != nil || got.Roots == nil || got.Roots.Root != b.StateRoot() || got.Roots.AppliedRoot != b.StateRoot() {
			t.Fatalf("profile root/account batch: %+v %v", got.Roots, err)
		}
		for _, account := range got.Accounts {
			want := expected[account.Address]
			if want == nil || !account.Exists || account.Nonce != want.nonce || account.Balance != want.balance.Dec() || account.CodeHash != crypto.Keccak256Hash(nil) {
				t.Fatalf("profile canonical ledger differs: %+v", account)
			}
			vector = append(vector, account)
		}
	}
	if len(vector) != len(addresses) {
		t.Fatal("profile missing canonical accounts")
	}
	if err := chain.DB().View(context.Background(), func(tx kv.Tx) error {
		n, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok || n != 1 || h != b.Hash() {
			return fmt.Errorf("profile applied marker differs")
		}
		senders, err := rawdb.ReadSenders(tx, b.Hash(), b.Number64().Uint64())
		if err != nil {
			return err
		}
		receipts, err := rawdb.ReadReceiptsWithError(tx, b, senders)
		if err != nil {
			return err
		}
		if len(receipts) != unixProfileTransactions {
			return fmt.Errorf("profile receipt count %d", len(receipts))
		}
		result := internal.ExecutedResultFor(chain.Config(), 1, b.StateRoot(), receipts)
		if result.ReceiptHash != b.Header().(*block.Header).ReceiptHash || result.GasUsed != unixProfileTransactions*21000 || result.Bloom != b.Header().(*block.Header).Bloom {
			return fmt.Errorf("profile receipt root/gas/bloom differs")
		}
		for _, txn := range b.Transactions() {
			value, err := tx.GetOne(modules.TxLookup, txn.Hash().Bytes())
			if err != nil {
				return err
			}
			if !bytes.Equal(value, b.Number64().Bytes()) {
				return fmt.Errorf("profile physical transaction lookup differs")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(vector)
	if err != nil {
		t.Fatal(err)
	}
	return types.Hash(sha256.Sum256(raw))
}
