// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

/**
 * @title IPQKeyRegistry
 * @dev Interface for Post-Quantum Public Key Registry
 *
 * This registry allows users to register their post-quantum public keys,
 * enabling subsequent transactions to reference the key by its hash
 * instead of including the full key (which can be large, e.g., 897B for Falcon-512).
 */
interface IPQKeyRegistry {
    /// @dev Emitted when a new public key is registered
    event KeyRegistered(
        address indexed account,
        bytes32 indexed keyHash,
        uint8 algorithm,
        uint256 timestamp
    );

    /// @dev Emitted when a public key is revoked
    event KeyRevoked(
        address indexed account,
        bytes32 indexed keyHash,
        uint256 timestamp
    );

    /**
     * @dev Register a post-quantum public key
     * @param pubKey The full public key bytes
     * @param algorithm The signature algorithm (0=Falcon, 1=SQIsign, 2=Dilithium2, 3=Dilithium3)
     * @return keyHash The keccak256 hash of the public key
     */
    function registerKey(bytes calldata pubKey, uint8 algorithm) external returns (bytes32 keyHash);

    /**
     * @dev Revoke a previously registered public key
     * @param keyHash The hash of the key to revoke
     */
    function revokeKey(bytes32 keyHash) external;

    /**
     * @dev Get the full public key by its hash
     * @param keyHash The hash of the public key
     * @return pubKey The full public key bytes
     */
    function getPublicKey(bytes32 keyHash) external view returns (bytes memory pubKey);

    /**
     * @dev Get the public key hash registered for an address
     * @param account The account address
     * @return keyHash The hash of the registered public key (0x0 if none)
     */
    function getKeyHashByAddress(address account) external view returns (bytes32 keyHash);

    /**
     * @dev Get the full public key registered for an address
     * @param account The account address
     * @return pubKey The full public key bytes
     */
    function getKeyByAddress(address account) external view returns (bytes memory pubKey);

    /**
     * @dev Check if a public key hash is registered
     * @param keyHash The hash to check
     * @return exists True if the key is registered
     */
    function hasKey(bytes32 keyHash) external view returns (bool exists);

    /**
     * @dev Check if an address has a registered key
     * @param account The address to check
     * @return exists True if the address has a registered key
     */
    function hasKeyForAddress(address account) external view returns (bool exists);

    /**
     * @dev Get the algorithm type for a registered key
     * @param keyHash The hash of the public key
     * @return algorithm The algorithm identifier
     */
    function getAlgorithm(bytes32 keyHash) external view returns (uint8 algorithm);

    /**
     * @dev Get the owner address of a registered key
     * @param keyHash The hash of the public key
     * @return owner The address that registered the key
     */
    function getKeyOwner(bytes32 keyHash) external view returns (address owner);
}
