// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

import "./IPQKeyRegistry.sol";

/**
 * @title PQKeyRegistry
 * @dev Implementation of Post-Quantum Public Key Registry
 *
 * This contract stores post-quantum public keys and allows transactions
 * to reference them by hash, significantly reducing transaction sizes
 * for users who make multiple transactions.
 *
 * Key sizes by algorithm:
 * - Falcon-512:  897 bytes
 * - SQIsign-I:   64 bytes
 * - Dilithium2:  1312 bytes
 * - Dilithium3:  1952 bytes
 *
 * After registration, subsequent transactions only need 32 bytes (hash)
 * instead of the full public key.
 */
contract PQKeyRegistry is IPQKeyRegistry {
    // Algorithm identifiers
    uint8 public constant ALGO_FALCON512 = 0;
    uint8 public constant ALGO_SQISIGN = 1;
    uint8 public constant ALGO_DILITHIUM2 = 2;
    uint8 public constant ALGO_DILITHIUM3 = 3;

    // Expected public key sizes for validation
    uint256 public constant FALCON512_PUBKEY_SIZE = 897;
    uint256 public constant SQISIGN_PUBKEY_SIZE = 64;
    uint256 public constant DILITHIUM2_PUBKEY_SIZE = 1312;
    uint256 public constant DILITHIUM3_PUBKEY_SIZE = 1952;

    // Key registration data
    struct KeyData {
        bytes pubKey;           // Full public key
        address owner;          // Address that registered the key
        uint8 algorithm;        // Algorithm identifier
        uint64 registeredAt;    // Registration timestamp
        bool revoked;           // Whether the key has been revoked
    }

    // keyHash => KeyData
    mapping(bytes32 => KeyData) private _keys;

    // address => keyHash (most recent active key)
    mapping(address => bytes32) private _addressToKeyHash;

    // address => keyHash[] (history of all keys)
    mapping(address => bytes32[]) private _addressKeyHistory;

    // Errors
    error InvalidAlgorithm(uint8 algorithm);
    error InvalidPublicKeySize(uint8 algorithm, uint256 expected, uint256 actual);
    error KeyAlreadyRegistered(bytes32 keyHash);
    error KeyNotFound(bytes32 keyHash);
    error NotKeyOwner(bytes32 keyHash, address caller);
    error KeyAlreadyRevoked(bytes32 keyHash);

    /**
     * @dev Register a post-quantum public key
     * @param pubKey The full public key bytes
     * @param algorithm The signature algorithm identifier
     * @return keyHash The keccak256 hash of the public key
     */
    function registerKey(bytes calldata pubKey, uint8 algorithm)
        external
        override
        returns (bytes32 keyHash)
    {
        // Validate algorithm
        if (algorithm > ALGO_DILITHIUM3) {
            revert InvalidAlgorithm(algorithm);
        }

        // Validate public key size
        uint256 expectedSize = _getExpectedKeySize(algorithm);
        if (pubKey.length != expectedSize) {
            revert InvalidPublicKeySize(algorithm, expectedSize, pubKey.length);
        }

        // Compute key hash
        keyHash = keccak256(pubKey);

        // Check if key already exists
        if (_keys[keyHash].registeredAt != 0) {
            revert KeyAlreadyRegistered(keyHash);
        }

        // Store key data
        _keys[keyHash] = KeyData({
            pubKey: pubKey,
            owner: msg.sender,
            algorithm: algorithm,
            registeredAt: uint64(block.timestamp),
            revoked: false
        });

        // Update address mapping
        _addressToKeyHash[msg.sender] = keyHash;
        _addressKeyHistory[msg.sender].push(keyHash);

        emit KeyRegistered(msg.sender, keyHash, algorithm, block.timestamp);

        return keyHash;
    }

    /**
     * @dev Revoke a previously registered public key
     * @param keyHash The hash of the key to revoke
     */
    function revokeKey(bytes32 keyHash) external override {
        KeyData storage keyData = _keys[keyHash];

        if (keyData.registeredAt == 0) {
            revert KeyNotFound(keyHash);
        }

        if (keyData.owner != msg.sender) {
            revert NotKeyOwner(keyHash, msg.sender);
        }

        if (keyData.revoked) {
            revert KeyAlreadyRevoked(keyHash);
        }

        keyData.revoked = true;

        // Clear address mapping if this was the active key
        if (_addressToKeyHash[msg.sender] == keyHash) {
            _addressToKeyHash[msg.sender] = bytes32(0);
        }

        emit KeyRevoked(msg.sender, keyHash, block.timestamp);
    }

    /**
     * @dev Get the full public key by its hash
     * @param keyHash The hash of the public key
     * @return pubKey The full public key bytes
     */
    function getPublicKey(bytes32 keyHash)
        external
        view
        override
        returns (bytes memory pubKey)
    {
        KeyData storage keyData = _keys[keyHash];
        if (keyData.registeredAt == 0) {
            revert KeyNotFound(keyHash);
        }
        return keyData.pubKey;
    }

    /**
     * @dev Get the public key hash registered for an address
     * @param account The account address
     * @return keyHash The hash of the registered public key
     */
    function getKeyHashByAddress(address account)
        external
        view
        override
        returns (bytes32 keyHash)
    {
        return _addressToKeyHash[account];
    }

    /**
     * @dev Get the full public key registered for an address
     * @param account The account address
     * @return pubKey The full public key bytes
     */
    function getKeyByAddress(address account)
        external
        view
        override
        returns (bytes memory pubKey)
    {
        bytes32 keyHash = _addressToKeyHash[account];
        if (keyHash == bytes32(0)) {
            return new bytes(0);
        }
        return _keys[keyHash].pubKey;
    }

    /**
     * @dev Check if a public key hash is registered
     * @param keyHash The hash to check
     * @return exists True if the key is registered and not revoked
     */
    function hasKey(bytes32 keyHash) external view override returns (bool exists) {
        KeyData storage keyData = _keys[keyHash];
        return keyData.registeredAt != 0 && !keyData.revoked;
    }

    /**
     * @dev Check if an address has a registered key
     * @param account The address to check
     * @return exists True if the address has an active registered key
     */
    function hasKeyForAddress(address account)
        external
        view
        override
        returns (bool exists)
    {
        bytes32 keyHash = _addressToKeyHash[account];
        if (keyHash == bytes32(0)) {
            return false;
        }
        return !_keys[keyHash].revoked;
    }

    /**
     * @dev Get the algorithm type for a registered key
     * @param keyHash The hash of the public key
     * @return algorithm The algorithm identifier
     */
    function getAlgorithm(bytes32 keyHash)
        external
        view
        override
        returns (uint8 algorithm)
    {
        KeyData storage keyData = _keys[keyHash];
        if (keyData.registeredAt == 0) {
            revert KeyNotFound(keyHash);
        }
        return keyData.algorithm;
    }

    /**
     * @dev Get the owner address of a registered key
     * @param keyHash The hash of the public key
     * @return owner The address that registered the key
     */
    function getKeyOwner(bytes32 keyHash)
        external
        view
        override
        returns (address owner)
    {
        KeyData storage keyData = _keys[keyHash];
        if (keyData.registeredAt == 0) {
            revert KeyNotFound(keyHash);
        }
        return keyData.owner;
    }

    /**
     * @dev Get all key hashes registered by an address
     * @param account The account address
     * @return keyHashes Array of all key hashes (including revoked)
     */
    function getKeyHistory(address account)
        external
        view
        returns (bytes32[] memory keyHashes)
    {
        return _addressKeyHistory[account];
    }

    /**
     * @dev Check if a key has been revoked
     * @param keyHash The hash of the public key
     * @return revoked True if the key has been revoked
     */
    function isRevoked(bytes32 keyHash) external view returns (bool revoked) {
        KeyData storage keyData = _keys[keyHash];
        if (keyData.registeredAt == 0) {
            revert KeyNotFound(keyHash);
        }
        return keyData.revoked;
    }

    /**
     * @dev Get registration timestamp for a key
     * @param keyHash The hash of the public key
     * @return timestamp The registration timestamp
     */
    function getRegistrationTime(bytes32 keyHash)
        external
        view
        returns (uint64 timestamp)
    {
        KeyData storage keyData = _keys[keyHash];
        if (keyData.registeredAt == 0) {
            revert KeyNotFound(keyHash);
        }
        return keyData.registeredAt;
    }

    /**
     * @dev Internal function to get expected key size for an algorithm
     * @param algorithm The algorithm identifier
     * @return size Expected public key size in bytes
     */
    function _getExpectedKeySize(uint8 algorithm)
        internal
        pure
        returns (uint256 size)
    {
        if (algorithm == ALGO_FALCON512) {
            return FALCON512_PUBKEY_SIZE;
        } else if (algorithm == ALGO_SQISIGN) {
            return SQISIGN_PUBKEY_SIZE;
        } else if (algorithm == ALGO_DILITHIUM2) {
            return DILITHIUM2_PUBKEY_SIZE;
        } else if (algorithm == ALGO_DILITHIUM3) {
            return DILITHIUM3_PUBKEY_SIZE;
        }
        return 0;
    }

    /**
     * @dev Get algorithm name as string (for debugging/display)
     * @param algorithm The algorithm identifier
     * @return name The algorithm name
     */
    function getAlgorithmName(uint8 algorithm)
        external
        pure
        returns (string memory name)
    {
        if (algorithm == ALGO_FALCON512) {
            return "Falcon-512";
        } else if (algorithm == ALGO_SQISIGN) {
            return "SQIsign-I";
        } else if (algorithm == ALGO_DILITHIUM2) {
            return "Dilithium2";
        } else if (algorithm == ALGO_DILITHIUM3) {
            return "Dilithium3";
        }
        return "Unknown";
    }
}
