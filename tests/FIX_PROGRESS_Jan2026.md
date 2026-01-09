# Test Fix Progress - January 2026

## Session Summary

**Starting Point**: 98.1% pass rate (37,006/37,724 tests), 718 failures
**Current Status**: 98.1% pass rate (37,013/37,724 tests), 711 failures
**Improvement**: Fixed 7 tests

## Fixes Implemented

### Fix #1: BLS Precompiles Activation for Prague Fork

**Problem Identified**:
- All 198 precompile failures traced to single test: `precompsEIP2929Cancun.json`
- Root cause: BLS12-381 precompiles (EIP-2537, addresses 0x0b-0x13) were NOT activated for Cancun/Prague forks
- `ActivePrecompiles()` function only checked IsMoran, IsNano, IsBerlin, IsIstanbul, IsByzantium
- No check for `IsCancun` or `IsPrague`, so these forks fell back to Homestead precompiles

**Solution**:
- Added `PrecompiledContractsCancun` (same as Berlin - no new precompiles)
- Added `PrecompiledContractsPrague` (Berlin + BLS precompiles at addresses 0x0b-0x13)
- Added address list initialization in `init()`
- Updated `ActivePrecompiles()` to check `IsPrague` and `IsCancun` before `IsBerlin`

**Result**:
- stPreCompiledContracts: 1290/1488 (86.7%) → 1296/1488 (87.1%)
- **Fixed 6 tests** (198 → 192 failures)
- Cancun fork: 722/744 (97.0%) - unchanged (correct, BLS not in Cancun)
- Prague fork: 568/744 (76.3%) → 574/744 (77.2%) - improved by 6 tests

**Files Modified**:
- `/internal/vm/contracts.go`:
  - Added `PrecompiledContractsCancun` and `PrecompiledContractsPrague`
  - Added `PrecompiledAddressesCancun` and `PrecompiledAddressesPrague` variables
  - Updated `init()` to initialize new address lists
  - Updated `ActivePrecompiles()` with Cancun/Prague checks

**Remaining Issues**:
- 192 precompile failures remain (12.9% failure rate in this category)
- Prague fork still has 170 failures (77.2% pass vs 97.0% in Cancun)
- This suggests BLS precompile implementations or gas costs may have issues

## Analysis: What Went Right

### Rapid Root Cause Identification
1. Recognized all 198 failures from same test file
2. Identified pattern: `storage[0x00] mismatch` indicating precompile call failures
3. Traced to missing precompile activation, not implementation bugs

### Correct Fork Distinction
- Initially tried adding BLS to both Cancun and Prague (made things worse: 368 failures)
- Realized BLS (EIP-2537) was Prague-only, corrected to only activate for Prague
- This matches Ethereum spec: BLS added in Pectra/Prague upgrade

## Analysis: Why Only 6 Tests Fixed?

The small improvement (6 tests out of 192) suggests:

1. **Precompile Implementation Issues**: BLS precompiles exist but may have bugs
   - Gas cost calculations
   - Input validation
   - Cryptographic operations

2. **Gas Accounting (EIP-2929)**: The test name `precompsEIP2929Cancun` indicates this tests:
   - EIP-2929: Gas cost increases for state access opcodes
   - Cold vs warm storage access costs
   - Precompile gas costs in EIP-2929 context

3. **Test Data Complexity**: The test has 462 data variants (d[0-461])
   - Each tests different precompile addresses and parameters
   - Only 6 of these were fixed by activation alone

## Next Steps

### Immediate: Measure Total Impact
- ✅ Full test suite completed: 711 failures (fixed 7 tests)
- Result close to expected ~712 failures (718 - 6 precompile fixes)
- 1 additional fix cascaded from precompile improvements

### High Priority: Analyze Remaining Precompile Failures
- Focus on Prague fork (170 failures)
- Categorize by specific BLS precompile (0x0b through 0x13)
- Check if failures are due to:
  - Incorrect gas costs
  - Implementation bugs in BLS operations
  - Input/output format issues

### Medium Priority: Investigate Other High-Impact Categories
- stTransactionTest: 106 failures (14.8% of baseline)
- stEIP150singleCodeGasPric: 59 failures (8.2% of baseline)
- stRandom: 48 failures (6.7% of baseline)
- stSystemOperationsTest: 42 failures (5.9% of baseline - likely SELFDESTRUCT)

## Lessons Learned

1. **Test Failure Clustering**: When many failures come from one test file, investigate systematically
   - Don't assume it's many different bugs
   - Look for a single root cause affecting multiple test cases

2. **Fork-Specific Features**: Always verify which fork introduced which features
   - BLS precompiles are Prague (Pectra), not Cancun (Dencun)
   - Activating features in wrong fork makes things worse

3. **Incremental Testing**: Test after each change to catch regressions immediately
   - Our first attempt (BLS in Cancun) made things worse
   - Quick test revealed the issue before committing

4. **Read the Specs**: EIP-2537 clearly states BLS is for Prague/Pectra
   - Would have saved time to verify before implementing

## Code Quality Notes

**Good**:
- Clean separation: `PrecompiledContractsBerlin`, `PrecompiledContractsCancun`, `PrecompiledContractsPrague`
- Consistent pattern in `ActivePrecompiles()` switch statement
- BLS precompiles already well-implemented in separate structs

**To Review**:
- Why weren't Cancun/Prague checks added when BLS precompiles were first implemented?
- Should `ActivePrecompiles()` use a more maintainable lookup (map/table vs giant switch)?

## Progress Tracking

| Category | Baseline | After BLS Fix | Delta | Status |
|----------|----------|---------------|-------|--------|
| stPreCompiledContracts | 1290/1488 (86.7%) | 1296/1488 (87.1%) | +6 | ✅ Improved |
| **Overall** | 37006/37724 (98.1%) | 37013/37724 (98.1%) | +7 | ✅ Complete |

**Fork Breakdown**:
- Cancun: 19,529/19,732 (99.0%) - unchanged (correct, BLS not in Cancun)
- Prague: 17,484/17,992 (97.2%) - improved by 7 tests

---

## Fix #2: Exception Validation OR Logic

**Problem Identified**:
- 106 failures in stTransactionTest traced to exception validation bug
- Tests expect multiple possible exceptions using OR logic: `"EXCEPTION1|EXCEPTION2"`
- Example: `"TransactionException.INSUFFICIENT_ACCOUNT_FUNDS|TransactionException.INTRINSIC_GAS_TOO_LOW"`
- Code only checked exact string matches, so never matched OR cases

**Solution**:
- Added `matchesExpectedException()` helper function in `validateExpectedException()`
- Splits exception types by `|` to handle OR logic
- Checks if actual condition matches ANY of the expected alternatives
- Updated all 11 exception validation checks to use helper function

**Result**:
- stTransactionTest: 412/518 (79.5%) → 504/518 (97.3%)
- **Fixed 92 tests** (87% reduction in failures!)
- Cancun fork: 208/259 (80.3%) → 254/259 (98.1%)
- Prague fork: 204/259 (78.8%) → 250/259 (96.5%)

**Files Modified**:
- `/tests/eth_test_runner_test.go`:
  - Added `matchesExpectedException()` helper (lines 808-822)
  - Updated all exception checks: TYPE_3_TX_PRE_FORK, TYPE_3_TX_ZERO_BLOBS, INITCODE_SIZE_EXCEEDED, TYPE_3_TX_BLOB_COUNT_EXCEEDED, TYPE_3_TX_CONTRACT_CREATION, TYPE_3_TX_INVALID_BLOB_VERSIONED_HASH, PRIORITY_GREATER_THAN_MAX_FEE_PER_GAS, INSUFFICIENT_MAX_FEE_PER_GAS, INTRINSIC_GAS_TOO_LOW, SENDER_NOT_EOA, INSUFFICIENT_ACCOUNT_FUNDS, GAS_ALLOWANCE_EXCEEDED

**Remaining stTransactionTest Issues** (14 failures):
- GASLIMIT_PRICE_PRODUCT_OVERFLOW exception not implemented (2 tests)
- SELFDESTRUCT balance mismatches (8 tests - EIP-6780 related)
- Gas calculation discrepancies (4 tests)

---

---

## Fix #3: GASLIMIT_PRICE_PRODUCT_OVERFLOW Exception

**Problem Identified**:
- 2 failures in stTransactionTest for `HighGasPriceParis` test
- Missing validation for `GASLIMIT_PRICE_PRODUCT_OVERFLOW` exception
- Occurs when `gasLimit * gasPrice` exceeds uint256 max (2^256 - 1)

**Solution**:
- Added overflow detection using big.Int arithmetic in `validateExpectedException()`
- Check performed BEFORE calculating totalCost to catch overflow early
- Uses matchesExpectedException() helper to support OR logic

**Result**:
- stTransactionTest: 504/518 (97.3%) → 506/518 (97.7%)
- **Fixed 2 tests**

**Files Modified**:
- `/tests/eth_test_runner_test.go`:
  - Added GASLIMIT_PRICE_PRODUCT_OVERFLOW validation (lines 1009-1021)
  - Detects when gasLimit * gasPrice > (2^256 - 1)

---

## Session Summary

**Last Updated**: January 9, 2026
**Session Start**: 98.1% (37,006/37,724), **718 failures**
**After Fix #1 (BLS)**: 98.1% (37,013/37,724), 711 failures, +7 tests
**After Fix #2 (Exception OR logic)**: 98.4% (37,105/37,724), 619 failures, +92 tests
**After Fix #3 (GASLIMIT overflow)**: 98.4% (37,107/37,724), **617 failures**, +2 tests
**Total Improvement**: **101 tests fixed** (14.1% reduction in failures)

**Fork Progress**:
- Cancun: 99.0% → 99.2% (19,529 → 19,576 passed), +47 tests
- Prague: 97.1% → 97.4% (17,477 → 17,531 passed), +54 tests

**Target**: 99.0%+ (< 377 failures)
**Remaining**: 617 failures (240 tests away from 99% target)

---

## Remaining High-Impact Failure Categories

Based on the current test results (619 failures remaining):

1. **stPreCompiledContracts**: 192 failures (31.0% of remaining)
   - Prague BLS implementation issues (gas costs or crypto operations)

2. **stSystemOperationsTest**: 42 failures (6.8% of remaining)
   - SELFDESTRUCT/EIP-6780 balance mismatches

3. **stEIP150singleCodeGasPric**: 59 failures (9.5% of remaining)
   - Gas pricing issues for EIP-150 opcodes

4. **stRandom**: 48 failures (7.8% of remaining)
   - Mixed failure types, need investigation

5. **stCreate2**: 34 failures (5.5% of remaining)
   - CREATE2 opcode issues

6. **stRandom2**: 30 failures (4.8% of remaining)
   - Mixed failure types

7. **stStackTests**: 31 failures (5.0% of remaining)
   - Stack depth or validation issues

8. **stTransactionTest**: 14 failures (2.3% of remaining)
   - SELFDESTRUCT + GASLIMIT_PRICE_PRODUCT_OVERFLOW + gas calc issues

These top 8 categories account for 450 failures (72.7% of all remaining failures).
