# N42 Blockchain Post-Quantum Upgrade Strategic Plan

## Version: 1.0
## Date: 2026-02-02

---

## Table of Contents

1. [Threat Model & Timeline](#1-threat-model--timeline)
2. [Current Cryptographic Dependency Analysis](#2-current-cryptographic-dependency-analysis)
3. [Seven Core Problems & Solutions](#3-seven-core-problems--solutions)
4. [Phased Implementation Roadmap](#4-phased-implementation-roadmap)
5. [Technical Specification Details](#5-technical-specification-details)
6. [Cost Model & Parameter Selection](#6-cost-model--parameter-selection)
7. [Wallet & Ecosystem Migration](#7-wallet--ecosystem-migration)
8. [Emergency Response Plan](#8-emergency-response-plan)

---

## 1. Threat Model & Timeline

### 1.1 Quantum Computing Threat Assessment

| Timeframe | Threat Level | Estimated Quantum Capability | Recommended Action |
|-----------|-------------|------------------------------|-------------------|
| 2026-2028 | Low | <100 logical qubits | Complete technical preparation |
| 2028-2030 | Medium | 500-1000 logical qubits | Begin protocol migration |
| 2030-2035 | High | 2000+ logical qubits | Complete full migration |
| 2035+ | Critical | Cryptographically relevant quantum computers | Migration must be complete |

**Key Data Points**:
- Breaking secp256k1 requires approximately 2,500 logical qubits (Shor's algorithm)
- IBM roadmap: 500-1,000 logical qubits by 2029
- Vitalik's estimate: 20% probability of a threat emerging before 2030

### 1.2 "Harvest Now, Decrypt Later" Threat

Attackers may collect encrypted data now and decrypt it once quantum computers mature:
- **Transaction signatures**: Once public keys are exposed, historical signatures can be analyzed
- **P2P communication**: Historical encrypted traffic can be decrypted
- **Staking credentials**: Validator public keys are visible long-term

---

## 2. Current Cryptographic Dependency Analysis

### 2.1 PQ Algorithm Libraries Already Integrated in N42 (Present but Not Enabled)

```
common/crypto/
├── dilithium/          ✅ Integrated
│   ├── mode2/          Signature: 2,420 B, Public key: 1,312 B
│   ├── mode3/          Signature: 3,293 B, Public key: 1,952 B
│   └── mode5/          Signature: 4,595 B, Public key: 2,592 B
├── kem/kyber/          ✅ Integrated
│   ├── kyber512/       Ciphertext: 768 B, Public key: 800 B
│   ├── kyber768/       Ciphertext: 1,088 B, Public key: 1,184 B
│   └── kyber1024/      Ciphertext: 1,568 B, Public key: 1,568 B
├── kem/frodo/          ✅ Integrated
│   └── frodo640shake/  Ciphertext: 9,720 B, Public key: 9,616 B
└── csidh/              ✅ Integrated (isogeny-based key exchange)
```

### 2.2 Currently Used Vulnerable Algorithms

| Component | Current Algorithm | Size | Quantum Threat | Replacement |
|-----------|------------------|------|---------------|-------------|
| Transaction signatures | ECDSA secp256k1 | 65 B | **Critical** | Dilithium-2/3 |
| Block signatures | ECDSA secp256k1 | 65 B | **Critical** | Dilithium-3 |
| Aggregate signatures | BLS12-381 | 96 B | **Critical** | Hash-based aggregation |
| P2P key exchange | ECDH secp256k1 | 33 B | **Critical** | Kyber-768 |
| Data commitments | KZG (BLS12-381) | 48 B | **High** | FRI/STARK |
| Address hashing | Keccak-256 | 32 B | Low | Retain (limited Grover speedup) |

### 2.3 Code Location Mapping

```
Transaction signatures:
├── common/transaction/transaction_signing.go    (Signing logic)
├── common/crypto/crypto.go                      (ECDSA implementation)
└── common/crypto/signature_cgo.go               (secp256k1 bindings)

Block signatures:
├── internal/consensus/misc/seal.go              (Block signing/verification)
├── internal/consensus/apos/apos.go              (APoS consensus)
└── common/crypto/bls/blst/signature.go          (BLS aggregate signatures)

P2P layer:
├── internal/p2p/discover/v5wire/crypto.go       (ECDH key exchange)
└── internal/p2p/handshake.go                    (Handshake protocol)

KZG commitments:
├── common/crypto/kzg/kzg.go                     (KZG implementation)
├── internal/vm/contracts_eip4844.go             (Precompiled contracts)
└── common/transaction/blob_tx.go                (Blob transactions)
```

---

## 3. Seven Core Problems & Solutions

### 3.1 Problem 1: Transaction Signature Migration

**Problem**: ECDSA secp256k1 signatures are completely vulnerable to quantum attacks

**Solution**: Introduce a new transaction type (Type 0x05)

```go
// New PQ transaction type definition
const PQTxType = 0x05

type PQTransaction struct {
    ChainID       *uint256.Int
    Nonce         uint64
    GasTipCap     *uint256.Int    // EIP-1559
    GasFeeCap     *uint256.Int    // EIP-1559
    Gas           uint64
    To            *types.Address
    Value         *uint256.Int
    Data          []byte
    AccessList    AccessList

    // PQ signature fields (replacing V, R, S)
    SignatureType uint8           // 0x01=Dilithium-2, 0x02=Dilithium-3
    PQPubKey      []byte          // 1,312 or 1,952 bytes
    PQSignature   []byte          // 2,420 or 3,293 bytes
}
```

**Address compatibility**: Maintain 20-byte addresses
```go
func PQPubkeyToAddress(pubkey []byte) types.Address {
    return types.BytesToAddress(crypto.Keccak256(pubkey)[12:])
}
```

**Signature verification cost model**:
| Operation | Current Gas | PQ Gas (estimated) | Reason |
|-----------|-----------|-------------------|--------|
| ECDSA verification | 3,000 | 3,000 | Unchanged |
| Dilithium-2 verification | - | 15,000 | ~5x computation |
| Dilithium-3 verification | - | 20,000 | Higher security level |

### 3.2 Problem 2: Post-Exposure Public Key Migration & Emergency Response

**Problem**: Ethereum-style addresses expose the public key upon the first transaction; on quantum day, funds can be stolen in bulk

**Solution A: Proactive migration mechanism**

```
┌─────────────────────────────────────────────────────────────┐
│ User's current state: EOA (ECDSA)                           │
│ Address: 0x1234...                                          │
│ Public key: Exposed (has transacted)                        │
└─────────────────────────────────────────────────────────────┘
                            ↓ Migration transaction
┌─────────────────────────────────────────────────────────────┐
│ Post-migration state: PQ-EOA (Dilithium)                    │
│ Address: 0x1234... (unchanged, via mapping)                 │
│ New control: Dilithium public key                           │
└─────────────────────────────────────────────────────────────┘
```

**Migration transaction format**:
```go
type MigrationTx struct {
    Type           uint8    // 0x06 migration transaction
    OldAddress     Address  // Original ECDSA address
    NewPQPubKey    []byte   // New Dilithium public key
    ECDSASignature []byte   // Old private key signature (proof of ownership)
    PQSignature    []byte   // New private key signature (proof of control)
}
```

**State storage**:
```go
// Account migration mapping (new state fields)
type Account struct {
    Nonce       uint64
    Balance     *uint256.Int
    Root        types.Hash
    CodeHash    types.Hash
    // New PQ fields
    PQMigrated  bool            // Whether migration is complete
    PQPubKeyHash types.Hash     // Dilithium public key hash
}
```

**Solution B: Quantum emergency hard fork**

Emergency plan when a quantum attack is detected:

```
Trigger conditions:
1. Large-scale signature forgery detected
2. Community consensus on emergency state

Emergency measures:
1. Freeze all unmigrated EOAs
2. Roll back suspicious transactions
3. Mandatory migration window (30-90 days)
4. Use "safety proofs" to unlock assets
```

**Safety proof mechanism**:
```go
type SafetyProof struct {
    // User must prove they owned the private key before a certain historical block
    // Sign the block hash with the private key
    BlockNumber    uint64
    BlockHash      types.Hash
    ECDSASignature []byte        // Old private key signature
    NewPQPubKey    []byte        // New PQ public key
    PQSignature    []byte        // New private key signature
}
```

### 3.3 Problem 3: Post-Quantum Consensus Layer Validator Signatures (BLS)

**Problem**: BLS12-381 public keys are visible long-term in consensus state and can be forged by quantum computers

**Current BLS usage**:
```go
// common/crypto/bls/blst/signature.go
const BLSSignatureLength = 96    // 96-byte signature
const BLSPubKeyLength = 48       // 48-byte public key

// Aggregation advantage: N signatures can be aggregated into a single 96-byte signature
```

**Alternative comparison**:

| Scheme | Single signature size | Aggregated size (1000 sigs) | Verification time |
|--------|----------------------|---------------------------|-------------------|
| BLS12-381 (current) | 96 B | 96 B | Fast |
| Dilithium-3 | 3,293 B | 3,293,000 B (no aggregation) | Medium |
| Hash-based aggregation | ~1,000 B | ~50,000 B (Merkle) | Medium |
| STARK aggregation | ~200 B | ~50,000 B (proof) | Slow |

**Recommended scheme: Hash-based Merkle aggregation**

```
Validator signature flow (post-quantum):

1. Each validator signs the block with Dilithium
2. Collect all signatures and build a Merkle tree
3. Only store the Merkle root + sampled proofs on-chain
4. Verification reconstructs partial Merkle paths

Advantages:
- Storage: O(log N) instead of O(N)
- Verification: Parallelizable
- Security: Relies only on hash functions
```

**Implementation structure**:
```go
type PQConsensusSignature struct {
    // Aggregate signature scheme
    MerkleRoot      types.Hash      // Signature Merkle tree root
    ParticipantBits []byte          // Participant bitmap
    Proofs          []MerkleProof   // Sampled Merkle proofs

    // Optional: STARK compressed proof
    STARKProof      []byte          // ~50KB
}
```

**Staking/withdrawal/slashing upgrades**:
```go
// Validator registration (post-quantum)
type PQValidator struct {
    PQPubKey        []byte          // Dilithium-3 public key
    WithdrawAddr    Address         // Withdrawal address (can be a PQ address)
    EffectiveBalance uint64

    // Slashing related
    Slashed         bool
    SlashingProof   []byte          // PQ-signed slashing proof
}
```

### 3.4 Problem 4: KZG Commitment Replacement (EIP-4844)

**Problem**: KZG polynomial commitments rely on pairing assumptions and are not quantum-safe

**Current KZG parameters** (common/crypto/kzg/kzg.go):
```go
const (
    FieldElementsPerBlob = 4096
    BytesPerBlob         = 131072    // 128 KB
    BytesPerCommitment   = 48        // BLS12-381 point
    BytesPerProof        = 48
)
```

**Alternative: FRI/STARK commitments**

```
KZG vs FRI comparison:

| Property | KZG | FRI |
|----------|-----|-----|
| Commitment size | 48 B | ~32 B |
| Proof size | 48 B | ~10 KB |
| Verification time | O(1) | O(log n) |
| Quantum safe | No | Yes |
| Trusted setup | Required | Not required |
```

**Migration strategy**:
```
Phase 1: Dual commitment period (compatibility)
- Blobs include both KZG and FRI commitments
- Validators verify both
- Clients can choose which to trust

Phase 2: FRI dominant
- FRI becomes the primary commitment
- KZG retained only for compatibility

Phase 3: Remove KZG
- Full switch to FRI
```

### 3.5 Problem 5: Post-Quantum Rollup/ZK Proof Systems

**Problem**: Groth16/Plonk rely on pairing curves and are not quantum-safe

**Current ZK dependencies**:
- Precompiled contracts 0x06-0x08: BN256 pairing
- Precompiled contracts 0x0b-0x13: BLS12-381 pairing (Prague+)

**Solution: STARK/FRI proof systems**

```
SNARK vs STARK:

| Property | SNARK (current) | STARK |
|----------|----------------|-------|
| Proof size | ~200 B | ~50 KB |
| Verification Gas | ~200K | ~500K-1M |
| Quantum safe | No | Yes |
| Trusted setup | Required | Not required |
| Recursion | Efficient | Feasible |
```

**Precompiled contract extensions**:
```go
// New STARK verification precompiles
const (
    STARKVerifyAddr = 0x15    // STARK proof verification
    FRIVerifyAddr   = 0x16    // FRI proof verification
)

// Gas cost model
func starkVerifyGas(proofSize int) uint64 {
    return uint64(500000 + proofSize * 10)  // Base + linear
}
```

### 3.6 Problem 6: Size/Fee/Throughput Engineering Challenges

**Problem**: PQ signatures are 37-50x larger, affecting throughput and cost

**Data bloat analysis**:
```
Current transaction (EIP-1559):
- Base: ~110 bytes
- Signature: 65 bytes
- Total: ~175 bytes

PQ transaction (Dilithium-2):
- Base: ~110 bytes
- Public key: 1,312 bytes
- Signature: 2,420 bytes
- Total: ~3,842 bytes

Bloat ratio: 22x
```

**Solution matrix**:

| Problem | Solution | Effect |
|---------|----------|--------|
| Large signature size | STARK aggregate compression | 10-50x compression |
| High verification cost | Batch verification precompile | 2-5x speedup |
| Bandwidth pressure | Public key caching/compression | Reduce duplicate transmission |
| Storage bloat | Store only public key hash | 1,312B -> 32B |
| Gas increase | Dynamic pricing adjustment | Market equilibrium |

**Signature aggregation scheme**:
```
┌─────────────────────────────────────────────────────────────┐
│ PQ transactions in a block                                  │
│ TX1: sig1 (2,420 B)                                         │
│ TX2: sig2 (2,420 B)                                         │
│ ...                                                         │
│ TXn: sign (2,420 B)                                         │
│ Total: n x 2,420 B                                          │
└─────────────────────────────────────────────────────────────┘
                            ↓ STARK aggregation
┌─────────────────────────────────────────────────────────────┐
│ Aggregate proof                                             │
│ - STARK proof that all signatures are valid: ~50 KB         │
│ - Public key commitment Merkle root: 32 B                   │
│ Total: ~50 KB (does not grow with transaction count)        │
└─────────────────────────────────────────────────────────────┘
```

**Gas cost model**:
```go
// New Gas calculation
func CalculatePQTxGas(tx *PQTransaction) uint64 {
    baseGas := uint64(21000)
    dataGas := uint64(len(tx.Data)) * 16

    // PQ signature verification Gas
    var sigVerifyGas uint64
    switch tx.SignatureType {
    case Dilithium2:
        sigVerifyGas = 15000
    case Dilithium3:
        sigVerifyGas = 20000
    }

    // Additional cost for first on-chain public key upload (first time only)
    if !IsPubKeyKnown(tx.PQPubKey) {
        sigVerifyGas += uint64(len(tx.PQPubKey)) * 4
    }

    return baseGas + dataGas + sigVerifyGas
}
```

### 3.7 Problem 7: Wallet & Ecosystem Migration

**Problem**: The entire ecosystem needs to support PQ keys and signatures

**Wallet support roadmap**:

```
Phase 1: Software wallets (6 months)
├── Key generation: Dilithium key pairs
├── Signing: PQ transaction signing
├── Address display: Compatible with existing format
└── Import/Export: New key format

Phase 2: Hardware wallets (12 months)
├── Ledger/Trezor firmware upgrades
├── Secure element support for Dilithium
└── Signing performance optimization

Phase 3: Smart contract wallets (6 months)
├── Account Abstraction integration
├── Multi-sig PQ support
└── Social recovery upgrades
```

**Key format standard**:
```
PQ private key format (Dilithium-2):
┌────────────────────────────────────────┐
│ Version: 1 byte (0x01)                 │
│ Type: 1 byte (0x01 = Dilithium-2)      │
│ Seed: 32 bytes                         │
│ Checksum: 4 bytes (Keccak256[:4])      │
│ Total: 38 bytes                        │
└────────────────────────────────────────┘

PQ address format (compatible):
- Still 20 bytes
- Format: Keccak256(PQPubKey)[12:]
- Display: 0x... (same as existing)
```

**"One-click migration" user flow**:
```
1. User opens wallet
2. Clicks "Upgrade to quantum-resistant"
3. Wallet generates new Dilithium key pair
4. Signs migration transaction (old private key + new private key)
5. Broadcasts migration transaction
6. Done! Address remains the same, control is transferred
```

---

## 4. Phased Implementation Roadmap

### Phase 0: Preparation (Now - 2026 Q3)

```
Goal: Technical validation and standard development

Tasks:
□ 1. Dilithium precompiled contract development and testing
□ 2. PQ transaction type specification definition
□ 3. Testnet deployment and stress testing
□ 4. Wallet SDK development
□ 5. Documentation and developer education

Deliverables:
- NIP-001: PQ Transaction Type Specification
- NIP-002: Migration Transaction Specification
- Testnet "quantum-testnet"
- Wallet SDK v1.0
```

### Phase 1: Optional Enablement (2026 Q4 - 2027 Q2)

```
Goal: Users can optionally use PQ signatures

Hard fork contents:
□ 1. Enable Type 0x05 PQ transactions
□ 2. Enable Type 0x06 migration transactions
□ 3. Add Dilithium precompiled contract (0x14)
□ 4. Add Kyber precompiled contract (0x15)

Compatibility:
- ECDSA transactions fully compatible
- Old wallets work normally
- New wallets can optionally use PQ

Gas adjustments:
- Dilithium-2 verification: 15,000 Gas
- Dilithium-3 verification: 20,000 Gas
- Migration transaction: 50,000 Gas
```

### Phase 2: Consensus Layer Upgrade (2027 Q3 - 2028 Q1)

```
Goal: Post-quantum validator signatures

Hard fork contents:
□ 1. Validator registration supports Dilithium
□ 2. Block signature dual mode (BLS + Dilithium)
□ 3. P2P layer Kyber key exchange
□ 4. Aggregate signature Merkle scheme

Transition strategy:
- New validators must use PQ
- Existing validators have a 6-month migration period
- Dual signature period ensures security
```

### Phase 3: Data Layer Upgrade (2028 Q2 - 2028 Q4)

```
Goal: KZG replacement and STARK support

Hard fork contents:
□ 1. Blob dual commitments (KZG + FRI)
□ 2. STARK verification precompile
□ 3. Signature aggregation STARK proofs

Rollup support:
- L2s can use STARK proofs
- Verification Gas optimization
- Recursive proof support
```

### Phase 4: Mandatory Migration (2029+)

```
Goal: Complete post-quantum transition

Measures:
□ 1. ECDSA transaction deprecation warning
□ 2. Unmigrated account restrictions
□ 3. Final removal of ECDSA support

Timeline:
- 2029 Q1: Deprecation warning
- 2029 Q3: New transactions must be PQ
- 2030 Q1: ECDSA fully disabled
```

---

## 5. Technical Specification Details

### 5.1 PQ Transaction RLP Encoding

```go
// Type 0x05 transaction RLP encoding
func (tx *PQTransaction) EncodeRLP(w io.Writer) error {
    return rlp.Encode(w, []interface{}{
        tx.ChainID,
        tx.Nonce,
        tx.GasTipCap,
        tx.GasFeeCap,
        tx.Gas,
        tx.To,
        tx.Value,
        tx.Data,
        tx.AccessList,
        tx.SignatureType,  // New
        tx.PQPubKey,       // New
        tx.PQSignature,    // New
    })
}

// Signing hash (for signing)
func (tx *PQTransaction) SigningHash(chainID *uint256.Int) types.Hash {
    return hash.PrefixedRlpHash(
        PQTxType,
        []interface{}{
            chainID,
            tx.Nonce,
            tx.GasTipCap,
            tx.GasFeeCap,
            tx.Gas,
            tx.To,
            tx.Value,
            tx.Data,
            tx.AccessList,
            tx.SignatureType,
            tx.PQPubKey,
        },
    )
}
```

### 5.2 Dilithium Precompiled Contract

```go
// contracts.go addition
var PrecompiledContractsPrague = map[types.Address]PrecompiledContract{
    // ... existing contracts ...
    types.BytesToAddress([]byte{0x14}): &dilithiumVerify{},
    types.BytesToAddress([]byte{0x15}): &kyberDecapsulate{},
}

// Dilithium verification precompile
type dilithiumVerify struct{}

func (d *dilithiumVerify) RequiredGas(input []byte) uint64 {
    if len(input) < 32 {
        return 0
    }
    sigType := input[0]
    switch sigType {
    case 0x01: // Dilithium-2
        return 15000
    case 0x02: // Dilithium-3
        return 20000
    default:
        return 0
    }
}

func (d *dilithiumVerify) Run(input []byte) ([]byte, error) {
    // Input format:
    // [0]: signature type (1 byte)
    // [1:33]: message hash (32 bytes)
    // [33:33+pubKeySize]: public key
    // [33+pubKeySize:]: signature

    if len(input) < 33 {
        return nil, errors.New("invalid input length")
    }

    sigType := input[0]
    msgHash := input[1:33]

    var pubKeySize, sigSize int
    switch sigType {
    case 0x01: // Dilithium-2
        pubKeySize = 1312
        sigSize = 2420
    case 0x02: // Dilithium-3
        pubKeySize = 1952
        sigSize = 3293
    default:
        return nil, errors.New("unknown signature type")
    }

    if len(input) < 33+pubKeySize+sigSize {
        return nil, errors.New("invalid input length")
    }

    pubKey := input[33 : 33+pubKeySize]
    sig := input[33+pubKeySize : 33+pubKeySize+sigSize]

    // Verify signature
    valid := dilithiumVerify(sigType, pubKey, msgHash, sig)

    if valid {
        return []byte{0x01}, nil
    }
    return []byte{0x00}, nil
}
```

### 5.3 Migration Transaction Processing

```go
// Migration handling in state transition
func (st *StateTransition) handleMigration(tx *MigrationTx) error {
    // 1. Verify old signature
    oldAddr := crypto.PubkeyToAddress(tx.OldPubKey)
    if !crypto.VerifySignature(tx.OldPubKey, tx.MigrationHash(), tx.ECDSASignature) {
        return errors.New("invalid ECDSA signature")
    }

    // 2. Verify new signature
    if !dilithium.Verify(tx.NewPQPubKey, tx.MigrationHash(), tx.PQSignature) {
        return errors.New("invalid PQ signature")
    }

    // 3. Update account state
    account := st.state.GetAccount(oldAddr)
    account.PQMigrated = true
    account.PQPubKeyHash = crypto.Keccak256Hash(tx.NewPQPubKey)
    st.state.SetAccount(oldAddr, account)

    // 4. Log migration event
    st.state.AddLog(&types.Log{
        Address: oldAddr,
        Topics:  []types.Hash{MigrationEventTopic},
        Data:    tx.NewPQPubKey,
    })

    return nil
}
```

---

## 6. Cost Model & Parameter Selection

### 6.1 Algorithm Selection Recommendations

| Use Case | Recommended Algorithm | Reason |
|----------|----------------------|--------|
| User transaction signatures | Dilithium-2 | Balance between size and security |
| Validator signatures | Dilithium-3 | Higher security level |
| P2P key exchange | Kyber-768 | NIST recommended level |
| Long-term storage signatures | Dilithium-3 | Future-proof security |

### 6.2 Gas Pricing Model

```go
const (
    // Base operations
    GasDilithium2Verify = 15000   // ~5x ECDSA
    GasDilithium3Verify = 20000   // ~7x ECDSA
    GasKyberDecap       = 10000   // Key decapsulation

    // Data storage
    GasPQPubKeyFirstUse = 5248    // 1312 * 4 (Dilithium-2)
    GasPQPubKeyCached   = 0       // Free when public key is already known

    // Aggregate verification
    GasSTARKVerifyBase  = 500000  // Base cost
    GasSTARKVerifyPerTx = 1000    // Per transaction
)
```

### 6.3 Block Capacity Impact

```
Current block (30M Gas, ~500 tx):
- Average transaction: ~175 bytes
- Block size: ~87.5 KB

PQ block (30M Gas):
- Without aggregation: ~78 tx (3,842 bytes each), 300 KB
- With STARK aggregation: ~300 tx, 150 KB (aggregate proof 50KB)

Recommendations:
- Short term: Maintain Gas limit, accept throughput reduction
- Medium term: Mandate STARK aggregation
- Long term: Increase Gas limit or optimize verification
```

---

## 7. Wallet & Ecosystem Migration

### 7.1 Wallet SDK Interface

```go
// PQ wallet interface
type PQWallet interface {
    // Key management
    GeneratePQKeyPair(algorithm string) (*PQKeyPair, error)
    ImportPQPrivateKey(data []byte) (*PQKeyPair, error)
    ExportPQPrivateKey(password string) ([]byte, error)

    // Signing
    SignPQTransaction(tx *PQTransaction) ([]byte, error)
    SignMigration(oldKey *ecdsa.PrivateKey, newKey *PQPrivateKey) (*MigrationTx, error)

    // Address
    GetPQAddress() types.Address
    GetPQPublicKey() []byte
}

// Key pair structure
type PQKeyPair struct {
    Algorithm   string   // "dilithium2", "dilithium3"
    PublicKey   []byte
    PrivateKey  []byte
    Address     types.Address
}
```

### 7.2 Migration User Flow

```
User experience flow:

1. Detection prompt
   ┌─────────────────────────────────────┐
   │ ⚠️ Quantum Security Upgrade Available│
   │                                     │
   │ Your account uses legacy signatures.│
   │ We recommend upgrading to quantum-  │
   │ resistant signatures to protect     │
   │ your assets.                        │
   │                                     │
   │ [Learn More] [Remind Later] [Upgrade│
   └─────────────────────────────────────┘

2. Backup confirmation
   ┌─────────────────────────────────────┐
   │ 📝 Please back up your new recovery │
   │    phrase                           │
   │                                     │
   │ 1. quantum  2. secure  3. wallet   │
   │ ...                                 │
   │                                     │
   │ [I have safely backed up]           │
   └─────────────────────────────────────┘

3. Signature confirmation
   ┌─────────────────────────────────────┐
   │ 🔐 Confirm Migration                │
   │                                     │
   │ From: 0x1234... (ECDSA)            │
   │ To:   0x1234... (Dilithium-2)      │
   │                                     │
   │ Gas fee: 0.001 ETH                  │
   │                                     │
   │ [Cancel]  [Confirm Migration]       │
   └─────────────────────────────────────┘

4. Completion
   ┌─────────────────────────────────────┐
   │ ✅ Migration Successful!             │
   │                                     │
   │ Your account is now protected with  │
   │ quantum-safe cryptography.          │
   │ Address unchanged: 0x1234...        │
   │                                     │
   │ [Done]                              │
   └─────────────────────────────────────┘
```

### 7.3 Hardware Wallet Support

```
Ledger/Trezor firmware requirements:

1. Storage expansion
   - Dilithium-2 private key: ~2.5 KB
   - Secure element must support larger keys

2. Signing performance
   - Dilithium signing: ~100ms (vs ECDSA ~50ms)
   - Acceptable user experience impact

3. Display adaptation
   - Public key display: Truncated display + hash checksum
   - Signature confirmation: Maintain existing flow

Timeline:
- 2027 Q1: Ledger Nano X firmware support
- 2027 Q2: Trezor Model T support
- 2027 Q4: Full product line support
```

---

## 8. Emergency Response Plan

### 8.1 Quantum Emergency Trigger Conditions

```go
type QuantumEmergency struct {
    Level       int       // Alert level 1-3
    TriggerTime time.Time
    Evidence    []byte    // Attack evidence
}

// Trigger conditions
const (
    Level1_Warning  = 1  // Suspicious patterns detected
    Level2_Alert    = 2  // Confirmed signature forgery attempt
    Level3_Critical = 3  // Large-scale attack in progress
)
```

### 8.2 Emergency Measures

**Level 1: Warning**
- Accelerate migration incentives
- Wallet pop-up warnings
- Community announcements

**Level 2: Alert**
- Transaction limits for unmigrated accounts
- Delayed confirmation for large transfers
- Mandatory validator upgrades

**Level 3: Critical**
```go
func HandleQuantumEmergency() {
    // 1. Pause all ECDSA transactions
    consensus.PauseECDSATransactions()

    // 2. Activate emergency recovery period
    state.SetEmergencyMode(true)

    // 3. Enable safety proofs for asset recovery
    state.EnableSafetyProofs()

    // 4. 30-day migration window
    state.SetMigrationDeadline(time.Now().Add(30 * 24 * time.Hour))
}
```

### 8.3 Asset Recovery Process

```
Safety proof recovery flow:

1. User proves they owned the private key before the attack
   - Select a block number before the attack
   - Sign the block hash with the old private key

2. Submit recovery request
   - Safety proof + new PQ public key
   - 7-day challenge period

3. Automatic recovery if unchallenged
   - Control transferred to new PQ key
   - Assets remain unchanged

Challenge mechanism:
- Anyone can submit counter-evidence
- Disputes resolved by governance vote
- Malicious challenges are penalized
```

---

## Appendix A: References

1. [NIST Post-Quantum Cryptography Standards](https://csrc.nist.gov/projects/post-quantum-cryptography)
2. [Ethereum Lean Ethereum Roadmap](https://blog.ethereum.org/2025/07/31/lean-ethereum)
3. [EIP-7693: Backward-Compatible Post-Quantum Migration](https://ethereum-magicians.org/t/eip-7693-backward-compatible-post-quantum-migration/19769)
4. [Hybrid Post-Quantum Signatures for Bitcoin and Ethereum](https://www.preprints.org/manuscript/202509.2079)

## Appendix B: Glossary

| Term | Definition |
|------|-----------|
| PQ | Post-Quantum |
| Dilithium | NIST-standardized lattice-based signature algorithm |
| Kyber | NIST-standardized lattice-based key encapsulation mechanism |
| KZG | Kate-Zaverucha-Goldberg polynomial commitment |
| FRI | Fast Reed-Solomon Interactive Oracle Proof |
| STARK | Scalable Transparent Argument of Knowledge |
| EOA | Externally Owned Account |

---

**Document Maintainer**: N42 Core Team
**Last Updated**: 2026-02-02
