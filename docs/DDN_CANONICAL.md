# DDN canonical protocol v1

Decisions confirmed on 2026-10-05: use JSON + Keccak256, support both node/CI
health classification and the separate governance EIP-712 compatibility format,
and call a sidecar on a separate machine over HTTP.

## Canonical bytes

The Go structs in `internal/ddn/types` define field order and JSON names.
All fields are present, including zero values. Encoding is compact UTF-8 JSON
without whitespace or a trailing newline. Strings must be valid UTF-8; there is
no Unicode normalization. Escape quotes, backslashes, control characters and
U+2028/U+2029 as Go `encoding/json` does; do not HTML-escape `<`, `>` or `&`.
Integers are unsigned base-10 JSON numbers (no exponent, no leading zeros).
Consumers must use integer parsers rather than JavaScript floating point for
uint64 values. Costs are unsigned decimal strings. Hashes are lowercase
`0x` followed by 64 hex digits. Probabilities/confidence are integers in ppm.
Array order is significant; nil arrays normalize to `[]`, never `null`.
Maps, arbitrary raw JSON and floating point fields are excluded from v1.

Request hashing sets `request_id` to the zero hash and `signature` to the empty
string. Receipt hashing sets `provider_signature` to the empty string. Manifest
hashing sets `signature` to the empty string. Those fields remain present in the
canonical document. The digest is Keccak256 of exactly these bytes; the request
ID equals this digest. Request and receipt include `chain_id` to prevent
cross-chain reuse. Receipt binds the request ID, input/policy hashes and nonce.

JSON decoding/re-encoding must precede digest calculation; the caller's original
JSON key order is not canonical. Unknown fields must be rejected at ingress.
This is a proposed DDN v1 interop format, not an assertion that n42-26 already
implements general DDN.

## Test vectors

`internal/ddn/types/testdata/canonical_v1.json` contains the complete canonical
byte strings and their digests for request, receipt and model manifest. Run
`go test ./internal/ddn/types` to verify them. The vectors include empty fields
and arrays and can be consumed directly by a future Rust implementation.

Governance `Quote` and `ResultAttestation` use their existing EIP-712 domain
`N42Decision`, version `1`, with the chain ID and DecisionHub address; they are
independent of this JSON format.

## Receipt keys and verification

Unsigned receipts are shadow diagnostics. Optional signing loads an explicitly
configured encrypted Web3 keystore file under a dedicated `ddn-keystore/`
directory (`0600`) using `N42_DDN_KEY_PASSWORD`; no validator/session/wallet key
is selected implicitly. The configured provider DID must equal
`did:n42:<lowercase signing address>`. This is an attestor's claim about a
sidecar model output, not TEE/ZKML proof of that model execution.

Consumers provide a trusted provider address and the originating request to
`receipt.Verify`, then use `verify.Verifier.Consume` for atomic replay rejection.
That bounded replay cache is in memory: durable settlement must store nonces
with the settlement transaction. General receipts use 65-byte signatures with
recovery ID 0/1 and low-S validation. Governance compatibility signatures use
27/28 for Solidity consumers. Hash, nonce, chain, time, model and escalation
checks occur before acceptance; signature recovery alone is insufficient.

`receipt/eip712_compat_test.go` uses fixed vectors generated from the actual
n42-26 relay, not from the Go implementation. Inputs mirror that relay's
`quote_cannot_move_refund_or_hub`, `attestation_signature_is_domain_bound`, and
`abi_answer_hash_matches_encoded_payload` tests.
