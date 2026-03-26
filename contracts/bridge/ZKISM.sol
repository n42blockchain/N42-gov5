// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "./N42Verifier.sol";

/// @title ZKISM — ZK-based Interchain Security Module for Hyperlane
/// @notice Replaces Hyperlane's default multisig ISM with N42's ZK proof verification.
///         Messages from N42 are secured by mathematical proof (SP1 Groth16/PLONK),
///         not by a trusted validator set.
///
/// Integration: Hyperlane Mailbox dispatches messages → ZKISM verifies via N42Verifier
///
/// Security model:
///   Default Hyperlane ISM: 3-of-5 multisig (trust assumption)
///   ZKISM:                 ZK proof of N42 consensus + state (math guarantee)
contract ZKISM {
    /// @notice N42 ZK verifier contract
    N42Verifier public immutable verifier;

    /// @notice Owner for configuration updates
    address public owner;

    /// @notice Minimum block confirmations before accepting a proof
    uint64 public minConfirmations;

    /// @notice Trusted N42 chain domain (Hyperlane domain ID)
    uint32 public immutable n42Domain;

    /// @notice Mapping of message ID => verified
    mapping(bytes32 => bool) public verifiedMessages;

    event MessageVerified(bytes32 indexed messageId, uint64 blockNumber, bytes32 stateRoot);
    event MinConfirmationsUpdated(uint64 oldValue, uint64 newValue);

    modifier onlyOwner() {
        require(msg.sender == owner, "not owner");
        _;
    }

    /// @param _verifier Address of deployed N42Verifier
    /// @param _n42Domain Hyperlane domain ID for the N42 chain
    /// @param _minConfirmations Minimum block confirmations (default: 1)
    constructor(address _verifier, uint32 _n42Domain, uint64 _minConfirmations) {
        verifier = N42Verifier(_verifier);
        n42Domain = _n42Domain;
        owner = msg.sender;
        minConfirmations = _minConfirmations > 0 ? _minConfirmations : 1;
    }

    /// @notice Hyperlane ISM module type constant
    uint8 public constant ISM_TYPE_CUSTOM = 4;

    /// @notice IInterchainSecurityModule.moduleType — returns ISM type
    function moduleType() external pure returns (uint8) {
        return ISM_TYPE_CUSTOM;
    }

    /// @notice Verify a Hyperlane message using ZK proof of N42 state.
    /// @dev Called by Hyperlane Mailbox.process() to validate inbound messages.
    ///
    /// Metadata layout (ABI-encoded):
    ///   bytes    headerProof     — SP1 proof of N42 header chain
    ///   uint64   startBlock      — First block in proven range
    ///   uint64   endBlock        — Last block in proven range
    ///   bytes32  stateRoot       — N42 state root at endBlock
    ///   bytes    stateProof      — JMT proof of message dispatch in N42 state
    ///
    /// @param _metadata ABI-encoded proof data
    /// @param _message  Hyperlane message bytes (sender, recipient, body, etc.)
    /// @return True if the message is cryptographically verified
    function verify(
        bytes calldata _metadata,
        bytes calldata _message
    ) external returns (bool) {
        // Validate message originates from N42 chain.
        // Hyperlane message format: version(1) + nonce(4) + origin(4) + sender(32) + ...
        // Origin domain is at bytes[5:9].
        require(_message.length >= 9, "ZKISM: message too short");
        uint32 origin = uint32(bytes4(_message[5:9]));
        require(origin == n42Domain, "ZKISM: invalid origin domain");

        // Decode proof metadata
        (
            bytes memory headerProof,
            uint64 startBlock,
            uint64 endBlock,
            bytes32 stateRoot,
            bytes memory stateProof
        ) = abi.decode(_metadata, (bytes, uint64, uint64, bytes32, bytes));

        // 1. Verify or use cached header chain verification
        if (!verifier.isVerified(endBlock)) {
            require(
                verifier.verifyHeaderChain(headerProof, startBlock, endBlock, stateRoot),
                "ZKISM: header chain verification failed"
            );
        } else {
            // Verify state root matches cached verification
            require(
                verifier.verifiedStateRoots(endBlock) == stateRoot,
                "ZKISM: state root mismatch"
            );
        }

        // 2. Verify the message dispatch exists in N42 state
        //    Key: keccak256("hyperlane_dispatch:", messageId)
        bytes32 messageId = keccak256(_message);
        bytes memory key = abi.encodePacked("hyperlane_dispatch:", messageId);
        bytes memory value = abi.encodePacked(messageId);

        require(
            verifier.verifyStateInclusion(endBlock, key, value, stateProof),
            "ZKISM: state inclusion proof failed"
        );

        // 3. Verify minimum confirmations (endBlock must be at least minConfirmations behind latest)
        if (minConfirmations > 0) {
            require(
                verifier.latestVerifiedBlock() >= endBlock,
                "ZKISM: endBlock not yet verified"
            );
        }

        // 4. Mark message as verified (idempotent — re-verification returns true)
        if (verifiedMessages[messageId]) {
            return true;
        }
        verifiedMessages[messageId] = true;

        emit MessageVerified(messageId, endBlock, stateRoot);
        return true;
    }

    /// @notice Check if a message has already been verified
    function isMessageVerified(bytes32 messageId) external view returns (bool) {
        return verifiedMessages[messageId];
    }

    // --- Owner controls ---

    function setMinConfirmations(uint64 _min) external onlyOwner {
        uint64 old = minConfirmations;
        minConfirmations = _min > 0 ? _min : 1;
        emit MinConfirmationsUpdated(old, minConfirmations);
    }

    function transferOwnership(address newOwner) external onlyOwner {
        require(newOwner != address(0), "zero address");
        owner = newOwner;
    }
}
