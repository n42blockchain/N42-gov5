# Test Suite Improvements

## Summary
- **Total Tests**: 37,724
- **Passed**: 37,006 (98.1%)
- **Failed**: 718 (1.9%)
- **Improvement**: +14 tests from previous run

## Fixed Categories

### stEIP3607 - EIP-3607 Sender Validation (100% ✓)
**Status**: 24/24 (100.0%) - **up from 10/24 (41.7%)**

**Fix Applied**:
- Implemented SENDER_NOT_EOA exception validation
- Accounts with code cannot initiate transactions (EIP-3607)
- Fixed address case sensitivity in pre-state lookup
- Reordered validation: SENDER_NOT_EOA before balance check

**Technical Details**:
- Pre-state lookup now uses lowercase addresses for JSON key matching
- Structural validations (SENDER_NOT_EOA) run before stateful checks (balance)
- Validation order: Intrinsic Gas → SENDER_NOT_EOA → Balance → Gas Limit

## Remaining Major Failures

| Category | Pass Rate | Failures |
|----------|-----------|----------|
| stSystemOperationsTest | 74.7% | 42 |
| stTransactionTest | 79.5% | 106 |
| stCallDelegateCodesCallCo | 82.8% | 20 |
| stPreCompiledContracts | 86.7% | 198 |
| stExtCodeHash | 87.0% | 18 |

## High Performance Categories (>99%)

- stTimeConsuming: 10380/10380 (100.0%)
- stZeroKnowledge: 1888/1888 (100.0%)
- stZeroKnowledge2: 1038/1038 (100.0%)
- stEIP3607: 24/24 (100.0%) ✨ **NEW**
- stEIP1559: 1942/1950 (99.6%)
- stMemoryTest: 1155/1156 (99.9%)
- stPreCompiledContracts2: 318/320 (99.4%)
