package trie

import (
	"bytes"
	"errors"
)

// Prove walks the trie along the nibble path of `key` and returns
// the RLP-encoded sequence of nodes traversed — exactly the shape
// required for EIP-1186 storage/account proofs.
//
//   - `key` is hashed-key BYTES (32 for account-keccak, 32 for
//     storage-slot-keccak). The function converts to nibbles
//     internally and adds the terminator.
//   - `fromLevel` skips this many proof nodes from the top before
//     emitting; useful when reusing an outer account-level proof as
//     the prefix of a storage proof.
//   - `storage` controls account-vs-storage descent: when true, on
//     reaching an AccountNode the walk continues into its Storage
//     sub-trie; when false the walk stops at the account.
//
// Returns the proof as `[][]byte`. Each element is the full RLP of
// one MPT node encountered along the path, root first.
//
// Ports the algorithm at
// C:/N42/erigon/execution/commitment/trie/proof.go (the Prove
// method on *Trie), adapted to our simplified Node set which has
// no DuoNode (HexPatriciaHashed always materialises FullNode).
func (t *Trie) Prove(key []byte, fromLevel int, storage bool) ([][]byte, error) {
	if t == nil {
		return nil, errors.New("Prove: nil trie")
	}
	nibKey := keybytesToHex(key)
	proof := make([][]byte, 0, 16)

	var tn Node = t.Root
	if tn == nil {
		// Witness trie may be empty if the requested key was never
		// touched. Caller decides whether that's an error.
		return proof, nil
	}

	for len(nibKey) > 0 && tn != nil {
		switch n := tn.(type) {
		case *ShortNode:
			if fromLevel == 0 {
				enc, err := encodeNode(n)
				if err != nil {
					return nil, err
				}
				proof = append(proof, enc)
			}
			nKey := n.Key
			if len(nKey) > 0 && nKey[len(nKey)-1] == 16 {
				nKey = nKey[:len(nKey)-1]
			}
			if len(nibKey) < len(nKey) || !bytes.Equal(nKey, nibKey[:len(nKey)]) {
				tn = nil // key diverges from the trie's short-node key
			} else {
				tn = n.Val
				nibKey = nibKey[len(nKey):]
			}
			if fromLevel > 0 {
				fromLevel--
			}

		case *FullNode:
			if fromLevel == 0 {
				enc, err := encodeNode(n)
				if err != nil {
					return nil, err
				}
				proof = append(proof, enc)
			}
			tn = n.Children[nibKey[0]]
			nibKey = nibKey[1:]
			if fromLevel > 0 {
				fromLevel--
			}

		case *AccountNode:
			if storage {
				tn = n.Storage
			} else {
				tn = nil
			}

		case ValueNode:
			tn = nil

		case *HashNode:
			// We hit an unexpanded hash on the proof path — the
			// witness trie was not fully materialised for this key.
			// This is a witness-generation bug; surface it loudly.
			return nil, errors.New("Prove: unexpected HashNode on proof path (witness not fully expanded)")

		default:
			return nil, errors.New("Prove: unknown node type")
		}
	}
	return proof, nil
}

// keybytesToHex translates a packed byte slice to a hex-nibble
// representation with a terminator (16) appended. Mirrors erigon's
// nibbles.KeybytesToHex.
func keybytesToHex(b []byte) []byte {
	out := make([]byte, len(b)*2+1)
	for i, v := range b {
		out[i*2] = v >> 4
		out[i*2+1] = v & 0x0f
	}
	out[len(out)-1] = 16
	return out
}
