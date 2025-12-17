# Prediction Market Deployment Guide

This guide explains how to deploy and run prediction market applications (like Polymarket) on N42.

## Overview

N42 is fully EVM-compatible and supports all features required for prediction market applications:

- ✅ ERC-1155 (Conditional Tokens)
- ✅ ERC-20 (Collateral Tokens)
- ✅ CREATE2 (Deterministic Deployment)
- ✅ DELEGATECALL (Proxy Patterns)
- ✅ Events/Logs
- ✅ ERC-165 (Interface Detection)
- ✅ All standard precompiled contracts

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                  Prediction Market Stack                     │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐     │
│  │   Frontend  │    │   Backend   │    │   Oracle    │     │
│  │  (Web/App)  │───▶│  (Indexer)  │◀───│  Service    │     │
│  └─────────────┘    └─────────────┘    └─────────────┘     │
│         │                  │                  │             │
│         └──────────────────┼──────────────────┘             │
│                            ▼                                │
│  ┌─────────────────────────────────────────────────────┐   │
│  │                    N42 Blockchain                    │   │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐     │   │
│  │  │ Conditional│  │   Market   │  │  Exchange  │     │   │
│  │  │   Tokens   │  │  Factory   │  │   (AMM)    │     │   │
│  │  │ (ERC-1155) │  │            │  │            │     │   │
│  │  └────────────┘  └────────────┘  └────────────┘     │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Core Contracts

### 1. Conditional Tokens (Gnosis CTF)

The Conditional Tokens Framework (CTF) is the foundation of prediction markets.

**Repository**: https://github.com/gnosis/conditional-tokens-contracts

**Key Functions**:

```solidity
// Prepare a condition (called by oracle)
function prepareCondition(
    address oracle,
    bytes32 questionId,
    uint outcomeSlotCount
) external;

// Split position (user places bet)
function splitPosition(
    IERC20 collateralToken,
    bytes32 parentCollectionId,
    bytes32 conditionId,
    uint[] calldata partition,
    uint amount
) external;

// Merge positions (user exits)
function mergePositions(
    IERC20 collateralToken,
    bytes32 parentCollectionId,
    bytes32 conditionId,
    uint[] calldata partition,
    uint amount
) external;

// Report outcome (called by oracle)
function reportPayouts(
    bytes32 questionId,
    uint[] calldata payouts
) external;

// Redeem winnings
function redeemPositions(
    IERC20 collateralToken,
    bytes32 parentCollectionId,
    bytes32 conditionId,
    uint[] calldata indexSets
) external;
```

### 2. Market Factory

Creates and manages prediction markets.

```solidity
interface IMarketFactory {
    function createMarket(
        address collateralToken,
        address oracle,
        bytes32 questionId,
        uint outcomeSlotCount,
        uint endTime
    ) external returns (bytes32 conditionId);
    
    function resolveMarket(
        bytes32 conditionId,
        uint[] calldata payouts
    ) external;
}
```

### 3. AMM (Automated Market Maker)

Provides liquidity for trading.

```solidity
interface IAMM {
    function buy(
        uint investmentAmount,
        uint outcomeIndex,
        uint minOutcomeTokensToBuy
    ) external returns (uint);
    
    function sell(
        uint returnAmount,
        uint outcomeIndex,
        uint maxOutcomeTokensToSell
    ) external returns (uint);
    
    function addLiquidity(
        uint addedFunds
    ) external returns (uint);
    
    function removeLiquidity(
        uint sharesToBurn
    ) external returns (uint);
}
```

## Deployment Steps

### Step 1: Deploy Conditional Tokens

```bash
# Using Foundry
forge create --rpc-url $N42_RPC \
    --private-key $DEPLOYER_KEY \
    src/ConditionalTokens.sol:ConditionalTokens

# Using Hardhat
npx hardhat run scripts/deploy-ctf.js --network n42
```

### Step 2: Deploy Collateral Token (or use existing)

If using a new collateral token:

```solidity
// Deploy USDC-like token
contract CollateralToken is ERC20 {
    constructor() ERC20("USD Coin", "USDC") {
        _mint(msg.sender, 1000000000 * 10**6); // 1B tokens
    }
    
    function decimals() public pure override returns (uint8) {
        return 6;
    }
}
```

### Step 3: Deploy Market Factory

```solidity
contract MarketFactory {
    ConditionalTokens public ctf;
    
    constructor(address _ctf) {
        ctf = ConditionalTokens(_ctf);
    }
    
    function createMarket(
        address oracle,
        bytes32 questionId,
        uint outcomeSlotCount
    ) external returns (bytes32) {
        bytes32 conditionId = ctf.getConditionId(
            oracle,
            questionId,
            outcomeSlotCount
        );
        ctf.prepareCondition(oracle, questionId, outcomeSlotCount);
        return conditionId;
    }
}
```

### Step 4: Deploy AMM

```solidity
contract FixedProductMarketMaker {
    ConditionalTokens public ctf;
    IERC20 public collateralToken;
    bytes32 public conditionId;
    
    // Constant product formula: x * y = k
    function buy(
        uint investmentAmount,
        uint outcomeIndex,
        uint minOutcomeTokensToBuy
    ) external returns (uint outcomeTokensBought) {
        // Implementation
    }
}
```

## Oracle Integration

### Option 1: UMA Optimistic Oracle

```solidity
interface IOptimisticOracle {
    function requestPrice(
        bytes32 identifier,
        uint256 timestamp,
        bytes memory ancillaryData,
        IERC20 currency,
        uint256 reward
    ) external returns (uint256);
    
    function proposePrice(
        address requester,
        bytes32 identifier,
        uint256 timestamp,
        bytes memory ancillaryData,
        int256 proposedPrice
    ) external;
}
```

### Option 2: Chainlink

```solidity
interface AggregatorV3Interface {
    function latestRoundData() external view returns (
        uint80 roundId,
        int256 answer,
        uint256 startedAt,
        uint256 updatedAt,
        uint80 answeredInRound
    );
}
```

### Option 3: Custom Oracle

```solidity
contract CustomOracle {
    mapping(bytes32 => uint[]) public outcomes;
    mapping(bytes32 => bool) public resolved;
    
    function resolve(
        bytes32 questionId,
        uint[] calldata payouts
    ) external onlyAuthorized {
        require(!resolved[questionId], "Already resolved");
        outcomes[questionId] = payouts;
        resolved[questionId] = true;
    }
}
```

## Example: Binary Market

Create a Yes/No prediction market:

```solidity
// 1. Create condition
bytes32 questionId = keccak256("Will ETH reach $5000 by end of 2025?");
uint outcomeSlotCount = 2; // Yes or No

ctf.prepareCondition(oracle, questionId, outcomeSlotCount);

// 2. User bets on "Yes"
uint[] memory partition = new uint[](2);
partition[0] = 1; // Yes
partition[1] = 2; // No

ctf.splitPosition(
    usdc,           // collateral
    bytes32(0),     // parent collection
    conditionId,
    partition,
    100 * 10**6     // 100 USDC
);

// 3. Oracle resolves (Yes wins)
uint[] memory payouts = new uint[](2);
payouts[0] = 1; // Yes wins
payouts[1] = 0; // No loses

ctf.reportPayouts(questionId, payouts);

// 4. Winner redeems
uint[] memory indexSets = new uint[](1);
indexSets[0] = 1; // Yes position

ctf.redeemPositions(usdc, bytes32(0), conditionId, indexSets);
```

## Gas Optimization Tips

1. **Batch Operations**: Use `safeBatchTransferFrom` for multiple token transfers
2. **Minimal Proxy**: Use EIP-1167 clones for market deployment
3. **Off-chain Matching**: Match orders off-chain, settle on-chain

## Security Considerations

1. **Oracle Security**: Use decentralized oracles or dispute mechanisms
2. **Collateral Safety**: Audit collateral token contracts
3. **Reentrancy**: Use ReentrancyGuard for all external calls
4. **Flash Loans**: Consider flash loan attack vectors

## Reference Implementations

| Project | Repository |
|---------|------------|
| Gnosis CTF | https://github.com/gnosis/conditional-tokens-contracts |
| Polymarket | https://github.com/Polymarket |
| Omen | https://github.com/protofire/omen-exchange |
| UMA Oracle | https://github.com/UMAprotocol/protocol |

## Testing

Run compatibility tests:

```bash
# Run prediction market compatibility tests
go test ./tests/... -run PredictionMarket -v

# Run all tests
go test ./tests/... -v
```

## RPC Endpoints

| Method | Description |
|--------|-------------|
| `eth_call` | Query contract state |
| `eth_sendTransaction` | Submit transactions |
| `eth_getLogs` | Query events |
| `eth_getTransactionReceipt` | Get transaction results |

## Troubleshooting

### Common Issues

1. **"Execution reverted"**: Check gas limit and contract state
2. **"Insufficient funds"**: Ensure sufficient collateral approval
3. **"Invalid condition"**: Verify condition is prepared before splitting

### Debug Commands

```bash
# Check contract deployment
cast code $CONTRACT_ADDRESS --rpc-url $N42_RPC

# Call view function
cast call $CONTRACT_ADDRESS "balanceOf(address,uint256)" $USER $TOKEN_ID --rpc-url $N42_RPC

# Send transaction
cast send $CONTRACT_ADDRESS "splitPosition(address,bytes32,bytes32,uint256[],uint256)" \
    $COLLATERAL $PARENT $CONDITION "[1,2]" $AMOUNT \
    --rpc-url $N42_RPC --private-key $KEY
```

## Support

For questions or issues:
- GitHub Issues: [N42-gov5 Repository]
- Documentation: [docs/](.)

---

*Last updated: December 2024*

