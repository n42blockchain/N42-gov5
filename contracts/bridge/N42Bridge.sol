// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "./N42Verifier.sol";

/// @title N42Bridge — Trustless cross-chain bridge between N42 and Ethereum
/// @notice Users deposit ETH/ERC-20 on Ethereum (locked), receive tokens on N42.
///         To withdraw, they burn on N42 and submit a ZK proof to this contract.
///         The proof verifies: (1) N42 block headers are valid (HotStuff-2 QC),
///         (2) the burn event exists in N42 state (JMT Merkle proof).
///
/// Security: ZK proof = mathematical guarantee. No multisig, no oracle, no trust.
contract N42Bridge {
    N42Verifier public immutable verifier;
    address public owner;
    bool public paused;

    /// @notice Maximum single withdrawal amount (safety limit)
    uint256 public withdrawalLimit;

    /// @notice Time delay for large withdrawals (24h default)
    uint256 public largeWithdrawalDelay;

    /// @notice Threshold for "large" withdrawal
    uint256 public largeWithdrawalThreshold;

    /// @notice Processed withdrawal nonces (replay protection)
    mapping(bytes32 => bool) public processedWithdrawals;

    /// @notice Pending large withdrawals (time-locked)
    struct PendingWithdrawal {
        address recipient;
        uint256 amount;
        uint256 unlockTime;
        bool executed;
    }
    mapping(bytes32 => PendingWithdrawal) public pendingWithdrawals;

    /// @notice Total deposited per token (address(0) = ETH)
    mapping(address => uint256) public totalDeposited;

    event Deposit(address indexed sender, bytes32 indexed n42Recipient, uint256 amount, address token);
    event WithdrawalCompleted(bytes32 indexed withdrawalId, address indexed recipient, uint256 amount);
    event WithdrawalQueued(bytes32 indexed withdrawalId, address indexed recipient, uint256 amount, uint256 unlockTime);
    event Paused(address indexed by);
    event Unpaused(address indexed by);

    modifier onlyOwner() {
        require(msg.sender == owner, "not owner");
        _;
    }

    modifier whenNotPaused() {
        require(!paused, "paused");
        _;
    }

    constructor(address _verifier) {
        verifier = N42Verifier(_verifier);
        owner = msg.sender;
        withdrawalLimit = 100 ether;           // 100 ETH max per withdrawal
        largeWithdrawalDelay = 24 hours;       // 24h delay for large withdrawals
        largeWithdrawalThreshold = 10 ether;   // >10 ETH = large
    }

    /// @notice Deposit ETH to bridge to N42
    /// @param n42Recipient N42 address (32 bytes, left-padded)
    function deposit(bytes32 n42Recipient) external payable whenNotPaused {
        require(msg.value > 0, "zero deposit");
        require(n42Recipient != bytes32(0), "zero recipient");

        totalDeposited[address(0)] += msg.value;

        emit Deposit(msg.sender, n42Recipient, msg.value, address(0));
    }

    /// @notice Withdraw ETH using ZK proof of N42 burn event
    /// @param headerProof SP1 proof of N42 header chain
    /// @param startBlock First block in proven range
    /// @param endBlock Last block in proven range
    /// @param stateRoot N42 state root at endBlock
    /// @param stateProof JMT proof of burn event in N42 state
    /// @param recipient ETH address to receive funds
    /// @param amount Amount in wei
    /// @param withdrawalNonce Unique nonce for replay protection
    function withdraw(
        bytes calldata headerProof,
        uint64 startBlock,
        uint64 endBlock,
        bytes32 stateRoot,
        bytes calldata stateProof,
        address recipient,
        uint256 amount,
        bytes32 withdrawalNonce
    ) external whenNotPaused {
        require(amount > 0, "zero amount");
        require(amount <= withdrawalLimit, "exceeds limit");
        require(recipient != address(0), "zero recipient");
        require(!processedWithdrawals[withdrawalNonce], "already processed");

        // Step 1: Verify header chain (or use cached verification)
        if (!verifier.isVerified(endBlock)) {
            require(
                verifier.verifyHeaderChain(headerProof, startBlock, endBlock, stateRoot),
                "header chain verification failed"
            );
        } else {
            require(
                verifier.verifiedStateRoots(endBlock) == stateRoot,
                "state root mismatch"
            );
        }

        // Step 2: Verify burn event exists in N42 state
        bytes memory key = abi.encodePacked("bridge_withdrawal:", withdrawalNonce);
        bytes memory value = abi.encodePacked(recipient, amount);
        require(
            verifier.verifyStateInclusion(endBlock, key, value, stateProof),
            "state proof invalid"
        );

        // Step 3: Process withdrawal
        processedWithdrawals[withdrawalNonce] = true;
        totalDeposited[address(0)] -= amount;

        if (amount > largeWithdrawalThreshold) {
            // Large withdrawal: time-locked
            uint256 unlockTime = block.timestamp + largeWithdrawalDelay;
            pendingWithdrawals[withdrawalNonce] = PendingWithdrawal({
                recipient: recipient,
                amount: amount,
                unlockTime: unlockTime,
                executed: false
            });
            emit WithdrawalQueued(withdrawalNonce, recipient, amount, unlockTime);
        } else {
            // Small withdrawal: immediate
            (bool sent, ) = recipient.call{value: amount}("");
            require(sent, "transfer failed");
            emit WithdrawalCompleted(withdrawalNonce, recipient, amount);
        }
    }

    /// @notice Execute a time-locked large withdrawal after delay period
    function executeWithdrawal(bytes32 withdrawalNonce) external whenNotPaused {
        PendingWithdrawal storage pw = pendingWithdrawals[withdrawalNonce];
        require(pw.amount > 0, "not found");
        require(!pw.executed, "already executed");
        require(block.timestamp >= pw.unlockTime, "still locked");

        pw.executed = true;
        (bool sent, ) = pw.recipient.call{value: pw.amount}("");
        require(sent, "transfer failed");

        emit WithdrawalCompleted(withdrawalNonce, pw.recipient, pw.amount);
    }

    // --- Emergency controls ---

    function pause() external onlyOwner { paused = true; emit Paused(msg.sender); }
    function unpause() external onlyOwner { paused = false; emit Unpaused(msg.sender); }
    function setWithdrawalLimit(uint256 _limit) external onlyOwner { withdrawalLimit = _limit; }
    function setLargeThreshold(uint256 _t) external onlyOwner { largeWithdrawalThreshold = _t; }
    function setLargeDelay(uint256 _d) external onlyOwner { largeWithdrawalDelay = _d; }

    receive() external payable {}
}
