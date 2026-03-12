# N42 VM Update Guide

This document tracks the EVM implementation status and provides a guide for future updates based on geth/erigon developments.

## Current Implementation Status (v5.3.523)

### Supported Hard Forks

| Fork | Status | Key Features |
|------|--------|--------------|
| Frontier | ✅ Complete | Base opcodes |
| Homestead | ✅ Complete | DELEGATECALL |
| Tangerine Whistle | ✅ Complete | Gas cost adjustments |
| Spurious Dragon | ✅ Complete | EXP gas cost |
| Byzantium | ✅ Complete | STATICCALL, RETURNDATASIZE/COPY, REVERT |
| Constantinople | ✅ Complete | SHL, SHR, SAR, EXTCODEHASH, CREATE2 |
| Istanbul | ✅ Complete | CHAINID, SELFBALANCE |
| Berlin | ✅ Complete | EIP-2929 access lists |
| London | ✅ Complete | BASEFEE, EIP-3529 |
| Shanghai | ✅ Complete | PUSH0, EIP-3860 initcode |
| Cancun | ✅ Complete | TLOAD/TSTORE, MCOPY, BLOBHASH, BLOBBASEFEE |
| Prague | ✅ Complete | No new opcodes (inherits Cancun) |
| Pectra | ✅ Complete | EIP-7702, EIP-2935, EIP-2537 |
| Osaka | ✅ Complete | Full EOF support (9 EIPs) |
| Fusaka | ✅ Complete | CLZ, P-256 precompile, 48KB code limit |

### Implemented EIPs by Category

#### Core EVM (45+ EIPs)

**Arithmetic & Logic:**
- EIP-145: Bitwise shifting (SHL, SHR, SAR)
- EIP-1014: CREATE2
- EIP-1052: EXTCODEHASH
- EIP-1344: CHAINID
- EIP-3855: PUSH0

**Gas Metering:**
- EIP-150: Gas cost changes for IO operations
- EIP-1884: Repricing opcodes
- EIP-2200: Net metered SSTORE
- EIP-2929: Gas cost increases for state access
- EIP-3529: Reduction in refunds

**State & Storage:**
- EIP-1153: Transient storage (TLOAD/TSTORE)
- EIP-2935: Historical block hashes in state

**Blobs (EIP-4844):**
- BLOBHASH opcode
- BLOBBASEFEE opcode
- EIP-7691: Blob throughput increase

**Account Abstraction:**
- EIP-7702: Set EOA account code

**EOF (EVM Object Format) - Osaka:**
- EIP-3540: EOF v1 container format
- EIP-3670: EOF code validation
- EIP-4200: Static relative jumps (RJUMP, RJUMPI, RJUMPV)
- EIP-4750: EOF functions (CALLF, RETF, JUMPF)
- EIP-5450: EOF stack validation
- EIP-6206: JUMPF and non-returning functions
- EIP-663: Unlimited SWAP/DUP (DUPN, SWAPN, EXCHANGE)
- EIP-7480: Data section access (DATALOAD, DATALOADN, DATASIZE, DATACOPY)
- EIP-7620: EOF contract creation (EOFCREATE, RETURNCONTRACT)

**Fusaka Features:**
- EIP-7939: CLZ (Count Leading Zeros) opcode
- EIP-7951: secp256r1 (P-256) precompile
- EIP-7907: 48KB contract code size limit

### Opcode Implementation Matrix

| Range | Category | Count | Status |
|-------|----------|-------|--------|
| 0x00-0x0F | Stop & Arithmetic | 16 | ✅ |
| 0x10-0x1F | Comparison & Bitwise | 16 | ✅ |
| 0x20 | Keccak256 | 1 | ✅ |
| 0x30-0x3F | Environmental | 16 | ✅ |
| 0x40-0x4F | Block Information | 16 | ✅ |
| 0x50-0x5F | Stack/Memory/Storage | 16 | ✅ |
| 0x60-0x7F | Push Operations | 32 | ✅ |
| 0x80-0x8F | Dup Operations | 16 | ✅ |
| 0x90-0x9F | Swap Operations | 16 | ✅ |
| 0xA0-0xA4 | Log Operations | 5 | ✅ |
| 0xD0-0xD3 | EOF Data Section | 4 | ✅ |
| 0xE0-0xE8 | EOF Control Flow | 9 | ✅ |
| 0xEC | EOFCREATE | 1 | ✅ |
| 0xEE | RETURNCONTRACT | 1 | ✅ |
| 0xF0-0xFF | System Operations | 11 | ✅ |

### Precompiles

| Address | Name | EIP | Status |
|---------|------|-----|--------|
| 0x01 | ecRecover | - | ✅ |
| 0x02 | SHA256 | - | ✅ |
| 0x03 | RIPEMD160 | - | ✅ |
| 0x04 | Identity | - | ✅ |
| 0x05 | ModExp | EIP-198 | ✅ |
| 0x06 | ecAdd | EIP-196 | ✅ |
| 0x07 | ecMul | EIP-196 | ✅ |
| 0x08 | ecPairing | EIP-197 | ✅ |
| 0x09 | Blake2F | EIP-152 | ✅ |
| 0x0A | KZG Point Eval | EIP-4844 | ✅ |
| 0x0B-0x12 | BLS12-381 | EIP-2537 | ✅ |
| 0x100 | P256Verify | EIP-7951 | ✅ |

---

## Test Coverage Status

### Well Tested (✅)
- Basic arithmetic: ADD, SUB, MUL, DIV, MOD, EXP
- Comparison: LT, GT, EQ, ISZERO
- Bitwise: AND, OR, XOR, NOT
- Shifts: SHL, SHR, SAR
- EOF format validation
- Transient storage: TLOAD, TSTORE
- CLZ algorithm verification

### Needs Execution Tests (⚠️)
- EOF opcodes: RJUMP, RJUMPI, RJUMPV, CALLF, RETF, JUMPF
- Data section: DATALOAD, DATALOADN, DATASIZE, DATACOPY
- Stack manipulation: DUPN, SWAPN, EXCHANGE
- Contract creation: EOFCREATE, RETURNCONTRACT
- Standard opcodes: MLOAD, MSTORE, SLOAD, SSTORE, CALL, CREATE

---

## Update Checklist

When updating VM based on geth/erigon changes:

### 1. Research Phase
```bash
# Check geth releases
https://github.com/ethereum/go-ethereum/releases

# Check erigon releases
https://github.com/erigontech/erigon/releases

# Check EIP specifications
https://eips.ethereum.org/
```

### 2. Implementation Checklist

- [ ] **New Opcode**
  - [ ] Add opcode constant in `opcodes.go`
  - [ ] Implement execution function in `instructions.go` or `instructions_*.go`
  - [ ] Add to jump table in `jump_table.go` or `eips_*.go`
  - [ ] Add gas cost constants
  - [ ] Add stack requirements (numPop, numPush)
  - [ ] Add unit tests

- [ ] **New Precompile**
  - [ ] Implement in `contracts_*.go`
  - [ ] Register in `precompiles/registry.go`
  - [ ] Add gas calculation
  - [ ] Add unit tests with test vectors

- [ ] **Gas Cost Changes**
  - [ ] Update constants in `gas.go`
  - [ ] Update dynamic gas functions if needed
  - [ ] Verify backward compatibility

- [ ] **Hard Fork Activation**
  - [ ] Create new instruction set in `eips_*.go`
  - [ ] Add fork detection in chain config
  - [ ] Add tests for fork activation

### 3. Testing Checklist

```bash
# Run all VM tests
go test ./internal/vm/... -v

# Run with race detection
go test ./internal/vm/... -race

# Run benchmarks
go test ./internal/vm/... -bench=.

# Run Ethereum compatibility tests
go test ./tests/... -v -run TestFullStateTests
```

### 4. Documentation

- [ ] Update this guide with new EIPs
- [ ] Update `docs/jsonrpc/eth.md` if API changes
- [ ] Update CHANGELOG

---

## Key Source Files

| File | Purpose |
|------|---------|
| `internal/vm/opcodes.go` | Opcode constants (0x00-0xFF) |
| `internal/vm/instructions.go` | Core opcode implementations |
| `internal/vm/jump_table.go` | Instruction sets per fork |
| `internal/vm/eof.go` | EOF container parsing & validation |
| `internal/vm/eips.go` | EIP activation system |
| `internal/vm/eips_cancun.go` | Cancun EIPs |
| `internal/vm/eips_prague.go` | Prague EIPs |
| `internal/vm/eips_pectra.go` | Pectra EIPs |
| `internal/vm/eips_osaka.go` | Osaka/EOF EIPs |
| `internal/vm/eips_fusaka.go` | Fusaka EIPs |
| `internal/vm/gas.go` | Gas cost constants |
| `internal/vm/gas_table.go` | Gas cost tables per fork |
| `internal/vm/contracts_*.go` | Precompile implementations |

---

## Reference Resources

- [Ethereum EIPs](https://eips.ethereum.org/)
- [go-ethereum](https://github.com/ethereum/go-ethereum)
- [erigon](https://github.com/erigontech/erigon)
- [EOF Specification](https://github.com/ethereum/EIPs/blob/master/EIPS/eip-3540.md)
- [Pectra Upgrade](https://ethereum.org/roadmap/pectra/)

---

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 5.3.523 | 2026-02-01 | Pectra complete, Osaka EOF, Fusaka CLZ/P256 |
| 5.2.518 | 2026-01-25 | Security audit fixes |

---

*Last updated: 2026-02-01*
