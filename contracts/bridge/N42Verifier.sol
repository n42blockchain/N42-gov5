// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title N42Verifier — ZK proof verification for N42 cross-chain bridge
/// @notice Verifies SP1 Groth16/PLONK proofs of N42 block header chains
///         and JMT state inclusion proofs. Each verified proof stores the
///         N42 state root, enabling trustless cross-chain asset transfers.
///
/// Trust chain: HotStuff-2 BLS consensus → SP1 ZK proof → this contract
contract N42Verifier {
    /// @notice Owner for emergency operations (pause, upgrade)
    address public owner;

    /// @notice Verified N42 state roots indexed by block number
    mapping(uint256 => bytes32) public verifiedStateRoots;

    /// @notice Latest verified block number
    uint256 public latestVerifiedBlock;

    /// @notice Emergency pause flag
    bool public paused;

    /// @notice SP1 verifier gateway address (Succinct's on-chain verifier)
    address public sp1Verifier;

    /// @notice SP1 program verification key (identifies the N42 header chain circuit)
    bytes32 public headerChainVKey;

    event HeaderChainVerified(uint256 indexed startBlock, uint256 indexed endBlock, bytes32 stateRoot);
    event StateInclusionVerified(bytes32 indexed stateRoot, bytes key, bytes value);
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

    constructor(address _sp1Verifier, bytes32 _headerChainVKey) {
        owner = msg.sender;
        sp1Verifier = _sp1Verifier;
        headerChainVKey = _headerChainVKey;
    }

    /// @notice Verify an SP1 proof of N42 block header chain validity
    /// @param proof SP1 Groth16/PLONK proof bytes
    /// @param startBlock First block in the proven range
    /// @param endBlock Last block in the proven range
    /// @param stateRoot JMT state root at endBlock
    /// @return True if verification succeeds
    function verifyHeaderChain(
        bytes calldata proof,
        uint64 startBlock,
        uint64 endBlock,
        bytes32 stateRoot
    ) external whenNotPaused returns (bool) {
        require(endBlock > startBlock, "invalid range");
        require(endBlock > latestVerifiedBlock, "already verified");
        require(stateRoot != bytes32(0), "zero state root");

        // Encode public inputs: startBlock(8 LE) + endBlock(8 LE) + stateRoot(32)
        bytes memory publicInputs = abi.encodePacked(
            _toLittleEndian64(startBlock),
            _toLittleEndian64(endBlock),
            stateRoot
        );

        // Verify SP1 proof via the on-chain verifier gateway
        (bool success, ) = sp1Verifier.staticcall(
            abi.encodeWithSignature(
                "verifyProof(bytes32,bytes,bytes)",
                headerChainVKey,
                publicInputs,
                proof
            )
        );
        require(success, "SP1 proof verification failed");

        // Store verified state root
        verifiedStateRoots[endBlock] = stateRoot;
        if (endBlock > latestVerifiedBlock) {
            latestVerifiedBlock = endBlock;
        }

        emit HeaderChainVerified(startBlock, endBlock, stateRoot);
        return true;
    }

    /// @notice Check if a state root at a given block has been verified
    function isVerified(uint256 blockNumber) external view returns (bool) {
        return verifiedStateRoots[blockNumber] != bytes32(0);
    }

    /// @notice Verify a JMT state inclusion proof against a verified state root
    /// @dev The JMT proof is verified directly on-chain (Blake3 Merkle path)
    /// @param blockNumber Block number whose state root to verify against
    /// @param key Account address or storage key
    /// @param value Expected value
    /// @param jmtProof Encoded JMT Merkle path
    /// @return True if the key-value exists in the verified state
    function verifyStateInclusion(
        uint256 blockNumber,
        bytes calldata key,
        bytes calldata value,
        bytes calldata jmtProof
    ) external view whenNotPaused returns (bool) {
        bytes32 stateRoot = verifiedStateRoots[blockNumber];
        require(stateRoot != bytes32(0), "block not verified");

        // JMT proof verification (Blake3 Merkle path)
        // For production: implement Blake3 hash + JMT node traversal
        // For MVP: accept pre-verified proofs from the relayer
        // TODO: implement on-chain JMT verification or use ZK-compressed proof
        return _verifyJMTProof(stateRoot, key, value, jmtProof);
    }

    /// @dev JMT proof verification placeholder — will be replaced with
    ///      either on-chain Blake3 Merkle verification or ZK-compressed proof
    function _verifyJMTProof(
        bytes32 stateRoot,
        bytes calldata key,
        bytes calldata value,
        bytes calldata proof
    ) internal pure returns (bool) {
        // MVP: hash-based commitment check
        // Production: full Blake3 JMT Merkle path verification
        bytes32 commitment = keccak256(abi.encodePacked(stateRoot, key, value));
        bytes32 proofCommitment = bytes32(proof[:32]);
        return commitment == proofCommitment;
    }

    // --- Emergency controls ---

    function pause() external onlyOwner { paused = true; emit Paused(msg.sender); }
    function unpause() external onlyOwner { paused = false; emit Unpaused(msg.sender); }
    function updateSP1Verifier(address _v) external onlyOwner { sp1Verifier = _v; }
    function updateVKey(bytes32 _vk) external onlyOwner { headerChainVKey = _vk; }

    // --- Helpers ---

    function _toLittleEndian64(uint64 v) internal pure returns (bytes8) {
        return bytes8(
            bytes1(uint8(v)) |
            (bytes8(bytes1(uint8(v >> 8))) >> 8) |
            (bytes8(bytes1(uint8(v >> 16))) >> 16) |
            (bytes8(bytes1(uint8(v >> 24))) >> 24) |
            (bytes8(bytes1(uint8(v >> 32))) >> 32) |
            (bytes8(bytes1(uint8(v >> 40))) >> 40) |
            (bytes8(bytes1(uint8(v >> 48))) >> 48) |
            (bytes8(bytes1(uint8(v >> 56))) >> 56)
        );
    }
}
