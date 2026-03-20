# Code Review File Inventory

Generated on `2026-03-16T04:47:44Z` from `generate_code_review_inventory.sh`.

Excluded paths: `devtest/`, `mainnet/`, `n42data/`, `build/`, `bin/`, `coverage`, `.codex-cache/`, `.claude/`.

## Summary

- Total files: `2543`
- Go source files: `1887`
- Go test files: `429`

## Top-Level Distribution

- `lib`: 826
- `internal`: 788
- `common`: 411
- `modules`: 142
- `accounts`: 62
- `docs`: 55
- `api`: 51
- `cmd`: 38
- `conf`: 32
- `tests`: 27
- `contracts`: 20
- `params`: 13
- `utils`: 12
- `deployments`: 10
- `log`: 10
- `tools`: 9
- `scripts`: 7
- `.github`: 3
- `turbo`: 3
- `pkg`: 2
- `.dockerignore`: 1
- `.gitignore`: 1
- `.golangci.yml`: 1
- `.goreleaser.yml`: 1
- `AGENTS.md`: 1
- `CLAUDE.md`: 1
- `Dockerfile`: 1
- `Dockerfile.release`: 1
- `LICENSE`: 1
- `Makefile`: 1
- `README.md`: 1
- `SECURITY.md`: 1
- `VERSION`: 1
- `console`: 1
- `docker-compose.yml`: 1
- `go.mod`: 1
- `go.sum`: 1
- `interfaces.go`: 1
- `qodana.yaml`: 1
- `sdk`: 1
- `tps_benchmark_results.txt`: 1
- `tpsbench`: 1

## Functional Modules

### 00. Root, Governance, and CI (20 files)

#### `.github` (3 files)

- `.github/workflows/codeql.yml`
- `.github/workflows/main.yml`
- `.github/workflows/release.yml`
#### `root` (17 files)

- `interfaces.go`
- `AGENTS.md`
- `CLAUDE.md`
- `README.md`
- `SECURITY.md`
- `.dockerignore`
- `.gitignore`
- `.golangci.yml`
- `.goreleaser.yml`
- `Dockerfile`
- `Dockerfile.release`
- `LICENSE`
- `Makefile`
- `VERSION`
- `go.mod`
- `go.sum`
- `qodana.yaml`
### 01. Entry Points and Operator UX (101 files)

#### `accounts` (9 files)

- `accounts/accounts.go`
- `accounts/errors.go`
- `accounts/hd.go`
- `accounts/manager.go`
- `accounts/sort.go`
- `accounts/url.go`
- `accounts/accounts_test.go`
- `accounts/hd_test.go`
- `accounts/url_test.go`
#### `accounts/abi` (21 files)

- `accounts/abi/abi.go`
- `accounts/abi/argument.go`
- `accounts/abi/bind/auth.go`
- `accounts/abi/bind/backend.go`
- `accounts/abi/bind/base.go`
- `accounts/abi/bind/bind.go`
- `accounts/abi/bind/template.go`
- `accounts/abi/bind/util.go`
- `accounts/abi/doc.go`
- `accounts/abi/error.go`
- `accounts/abi/error_handling.go`
- `accounts/abi/event.go`
- `accounts/abi/method.go`
- `accounts/abi/pack.go`
- `accounts/abi/reflect.go`
- `accounts/abi/selector_parser.go`
- `accounts/abi/topics.go`
- `accounts/abi/type.go`
- `accounts/abi/unpack.go`
- `accounts/abi/utils.go`
- `accounts/abi/abi_fuzz_test.go`
#### `accounts/external` (1 files)

- `accounts/external/backend.go`
#### `accounts/keystore` (31 files)

- `accounts/keystore/account_cache.go`
- `accounts/keystore/file_cache.go`
- `accounts/keystore/key.go`
- `accounts/keystore/keystore.go`
- `accounts/keystore/passphrase.go`
- `accounts/keystore/plain.go`
- `accounts/keystore/presale.go`
- `accounts/keystore/wallet.go`
- `accounts/keystore/watch.go`
- `accounts/keystore/watch_fallback.go`
- `accounts/keystore/account_cache_test.go`
- `accounts/keystore/keystore_test.go`
- `accounts/keystore/passphrase_test.go`
- `accounts/keystore/plain_test.go`
- `accounts/keystore/testdata/dupes/1`
- `accounts/keystore/testdata/dupes/2`
- `accounts/keystore/testdata/dupes/foo`
- `accounts/keystore/testdata/keystore/.hiddenfile`
- `accounts/keystore/testdata/keystore/README`
- `accounts/keystore/testdata/keystore/UTC--2016-03-22T12-57-55.920751759Z--7ef5a6135f1fd6a02593eedc869c6d41d934aef8`
- `accounts/keystore/testdata/keystore/aaa`
- `accounts/keystore/testdata/keystore/empty`
- `accounts/keystore/testdata/keystore/foo/fd9bd350f08ee3c0c19b85a8e16114a11a60aa4e`
- `accounts/keystore/testdata/keystore/garbage`
- `accounts/keystore/testdata/keystore/no-address`
- `accounts/keystore/testdata/keystore/zero`
- `accounts/keystore/testdata/keystore/zzz`
- `accounts/keystore/testdata/v1/cb61d5a9c4896fb9658090b597ef0e7be6f7b67e/cb61d5a9c4896fb9658090b597ef0e7be6f7b67e`
- `accounts/keystore/testdata/v1_test_vector.json`
- `accounts/keystore/testdata/v3_test_vector.json`
- `accounts/keystore/testdata/very-light-scrypt.json`
#### `cmd/clef` (4 files)

- `cmd/clef/audit.go`
- `cmd/clef/main.go`
- `cmd/clef/rules.go`
- `cmd/clef/signer.go`
#### `cmd/evmsdk` (12 files)

- `cmd/evmsdk/blst.go`
- `cmd/evmsdk/common.go`
- `cmd/evmsdk/common_crypto.go`
- `cmd/evmsdk/common_test_utils.go`
- `cmd/evmsdk/common_verify.go`
- `cmd/evmsdk/verify.go`
- `cmd/evmsdk/ws.go`
- `cmd/evmsdk/common_lifecycle_test.go`
- `cmd/evmsdk/common_test.go`
- `cmd/evmsdk/common_verify_test.go`
- `cmd/evmsdk/verify_test.go`
- `cmd/evmsdk/ws_test.go`
#### `cmd/n42` (15 files)

- `cmd/n42/accountcmd.go`
- `cmd/n42/app.go`
- `cmd/n42/cmd.go`
- `cmd/n42/config.go`
- `cmd/n42/dbcmd.go`
- `cmd/n42/diskusage.go`
- `cmd/n42/diskusage_openbsd.go`
- `cmd/n42/diskusage_windows.go`
- `cmd/n42/exportcmd.go`
- `cmd/n42/flags.go`
- `cmd/n42/initcmd.go`
- `cmd/n42/logger.go`
- `cmd/n42/main.go`
- `cmd/n42/migratecmd.go`
- `cmd/n42/statecmd.go`
#### `cmd/stresstest` (1 files)

- `cmd/stresstest/main.go`
#### `cmd/utils` (3 files)

- `cmd/utils/prompt.go`
- `cmd/utils/tools.go`
- `cmd/utils/utils.go`
#### `cmd/verify` (2 files)

- `cmd/verify/main.go`
- `cmd/verify/verify.go`
#### `cmd/zkguest` (1 files)

- `cmd/zkguest/main.go`
#### `console` (1 files)

- `console/prompt/prompter.go`
### 02. Protocol Schemas and Public Interfaces (51 files)

#### `api/protocol` (51 files)

- `api/protocol/consensus_proto/consensus.pb.go`
- `api/protocol/consensus_proto/gen.go`
- `api/protocol/ext/gen.go`
- `api/protocol/ext/options.pb.go`
- `api/protocol/msg_proto/gen.go`
- `api/protocol/msg_proto/msg.pb.go`
- `api/protocol/state/gen.go`
- `api/protocol/state/state.pb.go`
- `api/protocol/sync_pb/blob_messages.go`
- `api/protocol/sync_pb/blob_messages_ssz.go`
- `api/protocol/sync_pb/gen.go`
- `api/protocol/sync_pb/generated.ssz.go`
- `api/protocol/sync_pb/hotstuff_messages.go`
- `api/protocol/sync_pb/hotstuff_messages_ssz.go`
- `api/protocol/sync_pb/snap_messages.go`
- `api/protocol/sync_pb/snap_messages_ssz.go`
- `api/protocol/sync_pb/snapshot_messages_ssz.go`
- `api/protocol/sync_pb/sync_pb.pb.go`
- `api/protocol/sync_pb/witness_messages.go`
- `api/protocol/sync_proto/gen.go`
- `api/protocol/sync_proto/sync.pb.go`
- `api/protocol/types_pb/blob_sidecar.go`
- `api/protocol/types_pb/gen.go`
- `api/protocol/types_pb/generated.ssz.go`
- `api/protocol/types_pb/types.pb.go`
- `api/protocol/sync_pb/blob_messages_test.go`
- `api/protocol/sync_pb/snap_messages_fuzz_test.go`
- `api/protocol/sync_pb/snap_messages_test.go`
- `api/protocol/consensus_proto/consensus.proto`
- `api/protocol/ext/options.proto`
- `api/protocol/include/google/api/annotations.proto`
- `api/protocol/include/google/api/http.proto`
- `api/protocol/include/google/api/httpbody.proto`
- `api/protocol/include/google/protobuf/any.proto`
- `api/protocol/include/google/protobuf/api.proto`
- `api/protocol/include/google/protobuf/compiler/plugin.proto`
- `api/protocol/include/google/protobuf/descriptor.proto`
- `api/protocol/include/google/protobuf/duration.proto`
- `api/protocol/include/google/protobuf/empty.proto`
- `api/protocol/include/google/protobuf/field_mask.proto`
- `api/protocol/include/google/protobuf/source_context.proto`
- `api/protocol/include/google/protobuf/struct.proto`
- `api/protocol/include/google/protobuf/timestamp.proto`
- `api/protocol/include/google/protobuf/type.proto`
- `api/protocol/include/google/protobuf/wrappers.proto`
- `api/protocol/msg_proto/msg.proto`
- `api/protocol/state/state.proto`
- `api/protocol/sync_pb/sync_pb.proto`
- `api/protocol/sync_proto/sync.proto`
- `api/protocol/types_pb/types.proto`
- `api/protocol/zkprover_pb/zkprover.proto`
### 03. Configuration and Network Parameters (45 files)

#### `conf` (32 files)

- `conf/account_config.go`
- `conf/bundler_config.go`
- `conf/checkpoint_config.go`
- `conf/config.go`
- `conf/consensus_config.go`
- `conf/database_config.go`
- `conf/defaults.go`
- `conf/dev_config.go`
- `conf/encrypted_pool_config.go`
- `conf/gasprice.go`
- `conf/genesis_config.go`
- `conf/graphql_config.go`
- `conf/history_expiry_config.go`
- `conf/layered_db_config.go`
- `conf/logger_config.go`
- `conf/mcp_config.go`
- `conf/metrics_config.go`
- `conf/mev_config.go`
- `conf/miner.go`
- `conf/node_config.go`
- `conf/p2p_config.go`
- `conf/peerdas_config.go`
- `conf/pprof_config.go`
- `conf/prune_config.go`
- `conf/snap_sync_config.go`
- `conf/snapshot_accel_config.go`
- `conf/snapshot_config.go`
- `conf/tracing_config.go`
- `conf/zkprover_config.go`
- `conf/logger_config_test.go`
- `conf/snap_sync_config_test.go`
- `conf/zkprover_config_test.go`
#### `params` (13 files)

- `params/bootnodes.go`
- `params/config.go`
- `params/config_rules.go`
- `params/dao.go`
- `params/denomination.go`
- `params/network_params.go`
- `params/networkname/network_name.go`
- `params/protocol_params.go`
- `params/version.go`
- `params/config_test.go`
- `params/chainspecs/hotstuff_testnet.json`
- `params/chainspecs/mainnet.json`
- `params/chainspecs/testnet.json`
### 04. Shared Domain Primitives (411 files)

#### `common` (10 files)

- `common/big.go`
- `common/blockchain.go`
- `common/engine.go`
- `common/events.go`
- `common/format.go`
- `common/gaspool.go`
- `common/interfaces.go`
- `common/peer_set.go`
- `common/state_types.go`
- `common/common_test.go`
#### `common/account` (2 files)

- `common/account/state_account.go`
- `common/account/state_account_test.go`
#### `common/avmtypes` (11 files)

- `common/avmtypes/access_list_tx.go`
- `common/avmtypes/block.go`
- `common/avmtypes/bloom9.go`
- `common/avmtypes/dynamic_fee_tx.go`
- `common/avmtypes/exchange.go`
- `common/avmtypes/legacy_tx.go`
- `common/avmtypes/logs.go`
- `common/avmtypes/transaction.go`
- `common/avmtypes/transaction_signing.go`
- `common/avmtypes/bloom9_test.go`
- `common/avmtypes/exchange_test.go`
#### `common/avmutil` (11 files)

- `common/avmutil/big.go`
- `common/avmutil/bytes.go`
- `common/avmutil/debug.go`
- `common/avmutil/format.go`
- `common/avmutil/path.go`
- `common/avmutil/size.go`
- `common/avmutil/test_utils.go`
- `common/avmutil/types.go`
- `common/avmutil/bytes_test.go`
- `common/avmutil/size_test.go`
- `common/avmutil/types_test.go`
#### `common/block` (13 files)

- `common/block/blob_sidecar.go`
- `common/block/block.go`
- `common/block/bloom9.go`
- `common/block/body.go`
- `common/block/header.go`
- `common/block/iblock.go`
- `common/block/log.go`
- `common/block/receipt.go`
- `common/block/block_test.go`
- `common/block/bloom9_test.go`
- `common/block/header_test.go`
- `common/block/log_test.go`
- `common/block/receipt_test.go`
#### `common/crypto` (278 files)

- `common/crypto/blake2b/blake2b.go`
- `common/crypto/blake2b/blake2bAVX2_amd64.go`
- `common/crypto/blake2b/blake2b_amd64.go`
- `common/crypto/blake2b/blake2b_f_fuzz.go`
- `common/crypto/blake2b/blake2b_generic.go`
- `common/crypto/blake2b/blake2b_ref.go`
- `common/crypto/blake2b/blake2x.go`
- `common/crypto/blake2b/register.go`
- `common/crypto/bls/bls.go`
- `common/crypto/bls/blst/aliases.go`
- `common/crypto/bls/blst/doc.go`
- `common/crypto/bls/blst/init.go`
- `common/crypto/bls/blst/public_key.go`
- `common/crypto/bls/blst/secret_key.go`
- `common/crypto/bls/blst/signature.go`
- `common/crypto/bls/blst/stub.go`
- `common/crypto/bls/common/constants.go`
- `common/crypto/bls/common/error.go`
- `common/crypto/bls/common/interface.go`
- `common/crypto/bls/constants.go`
- `common/crypto/bls/error.go`
- `common/crypto/bls/interface.go`
- `common/crypto/bls/lbs_ntest.go`
- `common/crypto/bls/signature_batch.go`
- `common/crypto/bls12381/arithmetic_decl.go`
- `common/crypto/bls12381/arithmetic_fallback.go`
- `common/crypto/bls12381/arithmetic_x86_adx.go`
- `common/crypto/bls12381/arithmetic_x86_noadx.go`
- `common/crypto/bls12381/bls12_381.go`
- `common/crypto/bls12381/field_element.go`
- `common/crypto/bls12381/fp.go`
- `common/crypto/bls12381/fp12.go`
- `common/crypto/bls12381/fp2.go`
- `common/crypto/bls12381/fp6.go`
- `common/crypto/bls12381/g1.go`
- `common/crypto/bls12381/g2.go`
- `common/crypto/bls12381/gt.go`
- `common/crypto/bls12381/isogeny.go`
- `common/crypto/bls12381/pairing.go`
- `common/crypto/bls12381/swu.go`
- `common/crypto/bls12381/utils.go`
- `common/crypto/bn256/bn256_fast.go`
- `common/crypto/bn256/bn256_slow.go`
- `common/crypto/bn256/cloudflare/bn256.go`
- `common/crypto/bn256/cloudflare/constants.go`
- `common/crypto/bn256/cloudflare/curve.go`
- `common/crypto/bn256/cloudflare/gfp.go`
- `common/crypto/bn256/cloudflare/gfp12.go`
- `common/crypto/bn256/cloudflare/gfp2.go`
- `common/crypto/bn256/cloudflare/gfp6.go`
- `common/crypto/bn256/cloudflare/gfp_decl.go`
- `common/crypto/bn256/cloudflare/gfp_generic.go`
- `common/crypto/bn256/cloudflare/lattice.go`
- `common/crypto/bn256/cloudflare/optate.go`
- `common/crypto/bn256/cloudflare/twist.go`
- `common/crypto/bn256/google/bn256.go`
- `common/crypto/bn256/google/constants.go`
- `common/crypto/bn256/google/curve.go`
- `common/crypto/bn256/google/gfp12.go`
- `common/crypto/bn256/google/gfp2.go`
- `common/crypto/bn256/google/gfp6.go`
- `common/crypto/bn256/google/optate.go`
- `common/crypto/bn256/google/twist.go`
- `common/crypto/crypto.go`
- `common/crypto/cryptopool/pool.go`
- `common/crypto/csidh/consts.go`
- `common/crypto/csidh/csidh.go`
- `common/crypto/csidh/curve.go`
- `common/crypto/csidh/doc.go`
- `common/crypto/csidh/fp511.go`
- `common/crypto/csidh/fp511_amd64.go`
- `common/crypto/csidh/fp511_generic.go`
- `common/crypto/csidh/fp511_noasm.go`
- `common/crypto/dilithium/dilithium.go`
- `common/crypto/dilithium/gen.go`
- `common/crypto/dilithium/internal/common/aes.go`
- `common/crypto/dilithium/internal/common/amd64.go`
- `common/crypto/dilithium/internal/common/asm/src.go`
- `common/crypto/dilithium/internal/common/field.go`
- `common/crypto/dilithium/internal/common/generic.go`
- `common/crypto/dilithium/internal/common/ntt.go`
- `common/crypto/dilithium/internal/common/pack.go`
- `common/crypto/dilithium/internal/common/params.go`
- `common/crypto/dilithium/internal/common/params/params.go`
- `common/crypto/dilithium/internal/common/poly.go`
- `common/crypto/dilithium/internal/common/stubs_amd64.go`
- `common/crypto/dilithium/mode2.go`
- `common/crypto/dilithium/mode2/dilithium.go`
- `common/crypto/dilithium/mode2/internal/dilithium.go`
- `common/crypto/dilithium/mode2/internal/mat.go`
- `common/crypto/dilithium/mode2/internal/pack.go`
- `common/crypto/dilithium/mode2/internal/params.go`
- `common/crypto/dilithium/mode2/internal/rounding.go`
- `common/crypto/dilithium/mode2/internal/sample.go`
- `common/crypto/dilithium/mode2/internal/vec.go`
- `common/crypto/dilithium/mode2aes.go`
- `common/crypto/dilithium/mode2aes/dilithium.go`
- `common/crypto/dilithium/mode2aes/internal/dilithium.go`
- `common/crypto/dilithium/mode2aes/internal/mat.go`
- `common/crypto/dilithium/mode2aes/internal/pack.go`
- `common/crypto/dilithium/mode2aes/internal/params.go`
- `common/crypto/dilithium/mode2aes/internal/rounding.go`
- `common/crypto/dilithium/mode2aes/internal/sample.go`
- `common/crypto/dilithium/mode2aes/internal/vec.go`
- `common/crypto/dilithium/mode3.go`
- `common/crypto/dilithium/mode3/dilithium.go`
- `common/crypto/dilithium/mode3/internal/dilithium.go`
- `common/crypto/dilithium/mode3/internal/mat.go`
- `common/crypto/dilithium/mode3/internal/pack.go`
- `common/crypto/dilithium/mode3/internal/params.go`
- `common/crypto/dilithium/mode3/internal/rounding.go`
- `common/crypto/dilithium/mode3/internal/sample.go`
- `common/crypto/dilithium/mode3/internal/vec.go`
- `common/crypto/dilithium/mode3aes.go`
- `common/crypto/dilithium/mode3aes/dilithium.go`
- `common/crypto/dilithium/mode3aes/internal/dilithium.go`
- `common/crypto/dilithium/mode3aes/internal/mat.go`
- `common/crypto/dilithium/mode3aes/internal/pack.go`
- `common/crypto/dilithium/mode3aes/internal/params.go`
- `common/crypto/dilithium/mode3aes/internal/rounding.go`
- `common/crypto/dilithium/mode3aes/internal/sample.go`
- `common/crypto/dilithium/mode3aes/internal/vec.go`
- `common/crypto/dilithium/mode5.go`
- `common/crypto/dilithium/mode5/dilithium.go`
- `common/crypto/dilithium/mode5/internal/dilithium.go`
- `common/crypto/dilithium/mode5/internal/mat.go`
- `common/crypto/dilithium/mode5/internal/pack.go`
- `common/crypto/dilithium/mode5/internal/params.go`
- `common/crypto/dilithium/mode5/internal/rounding.go`
- `common/crypto/dilithium/mode5/internal/sample.go`
- `common/crypto/dilithium/mode5/internal/vec.go`
- `common/crypto/dilithium/mode5aes.go`
- `common/crypto/dilithium/mode5aes/dilithium.go`
- `common/crypto/dilithium/mode5aes/internal/dilithium.go`
- `common/crypto/dilithium/mode5aes/internal/mat.go`
- `common/crypto/dilithium/mode5aes/internal/pack.go`
- `common/crypto/dilithium/mode5aes/internal/params.go`
- `common/crypto/dilithium/mode5aes/internal/rounding.go`
- `common/crypto/dilithium/mode5aes/internal/sample.go`
- `common/crypto/dilithium/mode5aes/internal/vec.go`
- `common/crypto/dilithium/templates/mode.templ.go`
- `common/crypto/dilithium/templates/modePkg.templ.go`
- `common/crypto/dilithium/templates/params.templ.go`
- `common/crypto/ecies/ecies.go`
- `common/crypto/ecies/params.go`
- `common/crypto/falcon/falcon.go`
- `common/crypto/falcon/internal.go`
- `common/crypto/keccakf1600/f1600x.go`
- `common/crypto/keccakf1600/f1600x2_arm64.go`
- `common/crypto/keccakf1600/f1600x4_amd64.go`
- `common/crypto/keccakf1600/f1600x4stubs_amd64.go`
- `common/crypto/keccakf1600/fallback.go`
- `common/crypto/keccakf1600/internal/asm/src.go`
- `common/crypto/kem/frodo/doc.go`
- `common/crypto/kem/frodo/frodo640shake/frodo.go`
- `common/crypto/kem/frodo/frodo640shake/matrix_shake.go`
- `common/crypto/kem/frodo/frodo640shake/noise.go`
- `common/crypto/kem/frodo/frodo640shake/util.go`
- `common/crypto/kem/kem.go`
- `common/crypto/kem/kyber/doc.go`
- `common/crypto/kem/kyber/gen.go`
- `common/crypto/kem/kyber/kyber1024/kyber.go`
- `common/crypto/kem/kyber/kyber512/kyber.go`
- `common/crypto/kem/kyber/kyber768/kyber.go`
- `common/crypto/kem/kyber/templates/pkg.templ.go`
- `common/crypto/kyber/internal/common/amd64.go`
- `common/crypto/kyber/internal/common/asm/src.go`
- `common/crypto/kyber/internal/common/field.go`
- `common/crypto/kyber/internal/common/generic.go`
- `common/crypto/kyber/internal/common/ntt.go`
- `common/crypto/kyber/internal/common/params.go`
- `common/crypto/kyber/internal/common/params/params.go`
- `common/crypto/kyber/internal/common/poly.go`
- `common/crypto/kyber/internal/common/sample.go`
- `common/crypto/kyber/internal/common/stubs_amd64.go`
- `common/crypto/kyber/kyber512/internal/cpapke.go`
- `common/crypto/kyber/kyber512/internal/mat.go`
- `common/crypto/kyber/kyber512/internal/params.go`
- `common/crypto/kyber/kyber512/internal/vec.go`
- `common/crypto/kyber/kyber512/kyber.go`
- `common/crypto/kzg/kzg.go`
- `common/crypto/kzg/kzg_peerdas.go`
- `common/crypto/pke/doc.go`
- `common/crypto/pke/kyber/gen.go`
- `common/crypto/pke/kyber/internal/common/amd64.go`
- `common/crypto/pke/kyber/internal/common/asm/src.go`
- `common/crypto/pke/kyber/internal/common/field.go`
- `common/crypto/pke/kyber/internal/common/generic.go`
- `common/crypto/pke/kyber/internal/common/ntt.go`
- `common/crypto/pke/kyber/internal/common/params.go`
- `common/crypto/pke/kyber/internal/common/params/params.go`
- `common/crypto/pke/kyber/internal/common/poly.go`
- `common/crypto/pke/kyber/internal/common/sample.go`
- `common/crypto/pke/kyber/internal/common/stubs_amd64.go`
- `common/crypto/pke/kyber/kyber.go`
- `common/crypto/pke/kyber/kyber1024/internal/cpapke.go`
- `common/crypto/pke/kyber/kyber1024/internal/mat.go`
- `common/crypto/pke/kyber/kyber1024/internal/params.go`
- `common/crypto/pke/kyber/kyber1024/internal/vec.go`
- `common/crypto/pke/kyber/kyber1024/kyber.go`
- `common/crypto/pke/kyber/kyber512/internal/cpapke.go`
- `common/crypto/pke/kyber/kyber512/internal/mat.go`
- `common/crypto/pke/kyber/kyber512/internal/params.go`
- `common/crypto/pke/kyber/kyber512/internal/vec.go`
- `common/crypto/pke/kyber/kyber512/kyber.go`
- `common/crypto/pke/kyber/kyber768/internal/cpapke.go`
- `common/crypto/pke/kyber/kyber768/internal/mat.go`
- `common/crypto/pke/kyber/kyber768/internal/params.go`
- `common/crypto/pke/kyber/kyber768/internal/vec.go`
- `common/crypto/pke/kyber/kyber768/kyber.go`
- `common/crypto/pke/kyber/templates/params.templ.go`
- `common/crypto/pke/kyber/templates/pkg.templ.go`
- `common/crypto/rand/rand.go`
- `common/crypto/sha3/doc.go`
- `common/crypto/sha3/hashes.go`
- `common/crypto/sha3/keccakf.go`
- `common/crypto/sha3/rc.go`
- `common/crypto/sha3/sha3.go`
- `common/crypto/sha3/shake.go`
- `common/crypto/sha3/xor.go`
- `common/crypto/sha3/xor_generic.go`
- `common/crypto/sha3/xor_unaligned.go`
- `common/crypto/signature_cgo.go`
- `common/crypto/stark/stark.go`
- `common/crypto/blake2b/blake2b_f_test.go`
- `common/crypto/blake2b/blake2b_test.go`
- `common/crypto/bls/blst/public_key_test.go`
- `common/crypto/bls/lbs_test.go`
- `common/crypto/bls12381/bls12_381_test.go`
- `common/crypto/bls12381/field_element_test.go`
- `common/crypto/bls12381/fp_test.go`
- `common/crypto/bn256/cloudflare/bn256_test.go`
- `common/crypto/bn256/cloudflare/example_test.go`
- `common/crypto/bn256/cloudflare/gfp_test.go`
- `common/crypto/bn256/cloudflare/lattice_test.go`
- `common/crypto/bn256/cloudflare/main_test.go`
- `common/crypto/bn256/google/bn256_test.go`
- `common/crypto/bn256/google/example_test.go`
- `common/crypto/bn256/google/main_test.go`
- `common/crypto/crypto_test.go`
- `common/crypto/ecies/ecies_test.go`
- `common/crypto/falcon/falcon_test.go`
- `common/crypto/kzg/kzg_peerdas_test.go`
- `common/crypto/kzg/kzg_test.go`
- `common/crypto/rand/rand_test.go`
- `common/crypto/signature_cgo_test.go`
- `common/crypto/stark/stark_test.go`
- `common/crypto/blake2b/blake2bAVX2_amd64.s`
- `common/crypto/blake2b/blake2b_amd64.s`
- `common/crypto/bls12381/arithmetic_x86.s`
- `common/crypto/bn256/LICENSE`
- `common/crypto/bn256/cloudflare/LICENSE`
- `common/crypto/bn256/cloudflare/gfp_amd64.s`
- `common/crypto/bn256/cloudflare/gfp_arm64.s`
- `common/crypto/bn256/cloudflare/mul_amd64.h`
- `common/crypto/bn256/cloudflare/mul_arm64.h`
- `common/crypto/bn256/cloudflare/mul_bmi2_amd64.h`
- `common/crypto/csidh/fp511_amd64.s`
- `common/crypto/csidh/testdata/csidh_testvectors.json`
- `common/crypto/dilithium/internal/common/amd64.s`
- `common/crypto/dilithium/internal/common/asm/go.mod`
- `common/crypto/dilithium/internal/common/asm/go.sum`
- `common/crypto/ecies/.gitignore`
- `common/crypto/ecies/LICENSE`
- `common/crypto/ecies/README`
- `common/crypto/keccakf1600/f1600x2_arm64.s`
- `common/crypto/keccakf1600/f1600x4_amd64.s`
- `common/crypto/keccakf1600/internal/asm/go.mod`
- `common/crypto/keccakf1600/internal/asm/go.sum`
- `common/crypto/kyber/internal/common/amd64.s`
- `common/crypto/kyber/internal/common/asm/go.mod`
- `common/crypto/kyber/internal/common/asm/go.sum`
- `common/crypto/pke/kyber/internal/common/amd64.s`
- `common/crypto/pke/kyber/internal/common/asm/go.mod`
- `common/crypto/pke/kyber/internal/common/asm/go.sum`
- `common/crypto/rand/BUILD.bazel`
- `common/crypto/sha3/sha3_s390x.s`
- `common/crypto/sha3/testdata/keccakKats.json.deflate`
#### `common/db` (1 files)

- `common/db/database.go`
#### `common/encoding` (1 files)

- `common/encoding/pool.go`
#### `common/ens` (2 files)

- `common/ens/ens.go`
- `common/ens/ens_test.go`
#### `common/hash` (1 files)

- `common/hash/hash.go`
#### `common/hexutil` (4 files)

- `common/hexutil/hexutil.go`
- `common/hexutil/json.go`
- `common/hexutil/hexutil_test.go`
- `common/hexutil/json_test.go`
#### `common/interface` (1 files)

- `common/interface/Transaction.go`
#### `common/math` (4 files)

- `common/math/big.go`
- `common/math/integer.go`
- `common/math/big_test.go`
- `common/math/integer_test.go`
#### `common/mclock` (4 files)

- `common/mclock/mclock.go`
- `common/mclock/simclock.go`
- `common/mclock/simclock_test.go`
- `common/mclock/mclock.s`
#### `common/message` (2 files)

- `common/message/message.go`
- `common/message/topic_default.go`
#### `common/metrics` (7 files)

- `common/metrics/collector.go`
- `common/metrics/exp.go`
- `common/metrics/parseing.go`
- `common/metrics/prometheus.go`
- `common/metrics/register.go`
- `common/metrics/registry.go`
- `common/metrics/set.go`
#### `common/paths` (2 files)

- `common/paths/paths.go`
- `common/paths/paths_test.go`
#### `common/prque` (6 files)

- `common/prque/lazyqueue.go`
- `common/prque/prque.go`
- `common/prque/sstack.go`
- `common/prque/lazyqueue_test.go`
- `common/prque/prque_test.go`
- `common/prque/sstack_test.go`
#### `common/rlp` (12 files)

- `common/rlp/decode.go`
- `common/rlp/doc.go`
- `common/rlp/encode.go`
- `common/rlp/iterator.go`
- `common/rlp/raw.go`
- `common/rlp/typecache.go`
- `common/rlp/decode_tail_test.go`
- `common/rlp/decode_test.go`
- `common/rlp/encode_test.go`
- `common/rlp/encoder_example_test.go`
- `common/rlp/iterator_test.go`
- `common/rlp/raw_test.go`
#### `common/transaction` (24 files)

- `common/transaction/access_list.go`
- `common/transaction/blob_tx.go`
- `common/transaction/dynamic_fee_tx.go`
- `common/transaction/legacy_tx.go`
- `common/transaction/message.go`
- `common/transaction/pool.go`
- `common/transaction/pq_optimization.go`
- `common/transaction/pq_signer.go`
- `common/transaction/pq_transaction.go`
- `common/transaction/sakuragi_tx.go`
- `common/transaction/setcode_tx.go`
- `common/transaction/transaction.go`
- `common/transaction/transaction_signing.go`
- `common/transaction/blob_tx_test.go`
- `common/transaction/dynamic_fee_tx_test.go`
- `common/transaction/legacy_tx_test.go`
- `common/transaction/pool_test.go`
- `common/transaction/pq_optimization_test.go`
- `common/transaction/pq_signer_test.go`
- `common/transaction/pq_transaction_test.go`
- `common/transaction/setcode_tx_test.go`
- `common/transaction/transaction_proto_test.go`
- `common/transaction/transaction_signing_test.go`
- `common/transaction/transaction_test.go`
#### `common/types` (14 files)

- `common/types/address.go`
- `common/types/bloom.go`
- `common/types/bytes.go`
- `common/types/hash.go`
- `common/types/int256.go`
- `common/types/signature.go`
- `common/types/size.go`
- `common/types/ssz/sszuint64.go`
- `common/types/address_test.go`
- `common/types/bloom_test.go`
- `common/types/bytes_test.go`
- `common/types/hash_test.go`
- `common/types/signature_test.go`
- `common/types/size_test.go`
#### `common/u256` (1 files)

- `common/u256/big.go`
### 05. Node Runtime and Core Services (788 files)

#### `internal` (27 files)

- `internal/block_validator.go`
- `internal/blockchain.go`
- `internal/blockchain_helpers.go`
- `internal/blockchain_insert.go`
- `internal/blockchain_reader.go`
- `internal/blockchain_reorg_audit.go`
- `internal/blockchain_types.go`
- `internal/blockchain_write.go`
- `internal/blockhelp.go`
- `internal/error.go`
- `internal/evm.go`
- `internal/forkchoice.go`
- `internal/genesis_block.go`
- `internal/hashing.go`
- `internal/interface.go`
- `internal/parallel_processor.go`
- `internal/prefetcher.go`
- `internal/state_processor.go`
- `internal/state_transition.go`
- `internal/types.go`
- `internal/block_validator_test.go`
- `internal/blockchain_helpers_test.go`
- `internal/blockchain_reorg_audit_test.go`
- `internal/blockchain_test.go`
- `internal/evm_test.go`
- `internal/forkchoice_test.go`
- `internal/prefetcher_test.go`
#### `internal/allocs` (2 files)

- `internal/allocs/mainnet.json`
- `internal/allocs/testnet.json`
#### `internal/amcdb` (8 files)

- `internal/amcdb/database.go`
- `internal/amcdb/lmdb/db_rw.go`
- `internal/amcdb/lmdb/errors.go`
- `internal/amcdb/lmdb/iterater.go`
- `internal/amcdb/lmdb/lmdb.go`
- `internal/amcdb/lmdb/snapshot_rw.go`
- `internal/amcdb/memdb/memdb.go`
- `internal/amcdb/lmdb/lmdb_test.go`
#### `internal/api` (62 files)

- `internal/api/account.go`
- `internal/api/addrlock.go`
- `internal/api/agg_sign.go`
- `internal/api/api.go`
- `internal/api/api_backend.go`
- `internal/api/api_misc.go`
- `internal/api/api_transaction.go`
- `internal/api/backend.go`
- `internal/api/basefee.go`
- `internal/api/block_args.go`
- `internal/api/blockscout.go`
- `internal/api/bundler_api.go`
- `internal/api/debug_trace.go`
- `internal/api/engine_api_blob.go`
- `internal/api/engine_api_v4.go`
- `internal/api/ens_api.go`
- `internal/api/eth_raw.go`
- `internal/api/feehistory.go`
- `internal/api/filters/api.go`
- `internal/api/filters/filter.go`
- `internal/api/filters/filter_query.go`
- `internal/api/filters/filter_system.go`
- `internal/api/filters/header_number.go`
- `internal/api/gasprice.go`
- `internal/api/graphql/graphql.go`
- `internal/api/graphql/handler.go`
- `internal/api/graphql/helpers.go`
- `internal/api/graphql/resolver.go`
- `internal/api/graphql/schema.go`
- `internal/api/header_number.go`
- `internal/api/interface.go`
- `internal/api/mev_api.go`
- `internal/api/router.go`
- `internal/api/rpc_extra.go`
- `internal/api/snapshot_api.go`
- `internal/api/stark_agg_sign.go`
- `internal/api/state_helpers.go`
- `internal/api/transaction_args.go`
- `internal/api/witness_api.go`
- `internal/api/zkproof_api.go`
- `internal/api/addrlock_test.go`
- `internal/api/agg_sign_test.go`
- `internal/api/api_bench_test.go`
- `internal/api/api_misc_test.go`
- `internal/api/api_test.go`
- `internal/api/backend_test.go`
- `internal/api/block_args_test.go`
- `internal/api/blockscout_test.go`
- `internal/api/debug_trace_test.go`
- `internal/api/engine_api_blob_test.go`
- `internal/api/ens_api_test.go`
- `internal/api/eth_methods_test.go`
- `internal/api/feehistory_test.go`
- `internal/api/filters/filter_system_test.go`
- `internal/api/filters/filter_test.go`
- `internal/api/filters/header_number_test.go`
- `internal/api/gasprice_test.go`
- `internal/api/interface_test.go`
- `internal/api/mev_api_test.go`
- `internal/api/rpc_extra_test.go`
- `internal/api/snapshot_api_test.go`
- `internal/api/stark_agg_sign_test.go`
#### `internal/avm` (82 files)

- `internal/avm/abi/abi.go`
- `internal/avm/abi/argument.go`
- `internal/avm/abi/doc.go`
- `internal/avm/abi/error.go`
- `internal/avm/abi/error_handling.go`
- `internal/avm/abi/event.go`
- `internal/avm/abi/method.go`
- `internal/avm/abi/pack.go`
- `internal/avm/abi/reflect.go`
- `internal/avm/abi/selector_parser.go`
- `internal/avm/abi/topics.go`
- `internal/avm/abi/type.go`
- `internal/avm/abi/unpack.go`
- `internal/avm/common/big.go`
- `internal/avm/common/bitutil/bitutil.go`
- `internal/avm/common/bitutil/compress.go`
- `internal/avm/common/bytes.go`
- `internal/avm/common/compiler/helpers.go`
- `internal/avm/common/compiler/solidity.go`
- `internal/avm/common/compiler/vyper.go`
- `internal/avm/common/debug.go`
- `internal/avm/common/fdlimit/fdlimit_bsd.go`
- `internal/avm/common/fdlimit/fdlimit_darwin.go`
- `internal/avm/common/fdlimit/fdlimit_unix.go`
- `internal/avm/common/fdlimit/fdlimit_windows.go`
- `internal/avm/common/format.go`
- `internal/avm/common/mclock/mclock.go`
- `internal/avm/common/mclock/simclock.go`
- `internal/avm/common/path.go`
- `internal/avm/common/prque/lazyqueue.go`
- `internal/avm/common/prque/prque.go`
- `internal/avm/common/prque/sstack.go`
- `internal/avm/common/size.go`
- `internal/avm/common/test_utils.go`
- `internal/avm/common/types.go`
- `internal/avm/error.go`
- `internal/avm/rlp/decode.go`
- `internal/avm/rlp/doc.go`
- `internal/avm/rlp/encode.go`
- `internal/avm/rlp/iterator.go`
- `internal/avm/rlp/raw.go`
- `internal/avm/rlp/typecache.go`
- `internal/avm/types/access_list_tx.go`
- `internal/avm/types/block.go`
- `internal/avm/types/bloom9.go`
- `internal/avm/types/dynamic_fee_tx.go`
- `internal/avm/types/exchange.go`
- `internal/avm/types/legacy_tx.go`
- `internal/avm/types/logs.go`
- `internal/avm/types/transaction.go`
- `internal/avm/types/transaction_signing.go`
- `internal/avm/abi/method_test.go`
- `internal/avm/abi/pack_test.go`
- `internal/avm/abi/packing_test.go`
- `internal/avm/abi/reflect_test.go`
- `internal/avm/abi/selector_parser_test.go`
- `internal/avm/abi/topics_test.go`
- `internal/avm/abi/type_test.go`
- `internal/avm/abi/unpack_test.go`
- `internal/avm/common/bitutil/bitutil_test.go`
- `internal/avm/common/bitutil/compress_test.go`
- `internal/avm/common/bytes_test.go`
- `internal/avm/common/compiler/solidity_test.go`
- `internal/avm/common/compiler/vyper_test.go`
- `internal/avm/common/fdlimit/fdlimit_test.go`
- `internal/avm/common/mclock/simclock_test.go`
- `internal/avm/common/prque/lazyqueue_test.go`
- `internal/avm/common/prque/prque_test.go`
- `internal/avm/common/prque/sstack_test.go`
- `internal/avm/common/size_test.go`
- `internal/avm/common/types_test.go`
- `internal/avm/rlp/decode_tail_test.go`
- `internal/avm/rlp/decode_test.go`
- `internal/avm/rlp/encode_test.go`
- `internal/avm/rlp/encoder_example_test.go`
- `internal/avm/rlp/iterator_test.go`
- `internal/avm/rlp/raw_test.go`
- `internal/avm/types/bloom9_test.go`
- `internal/avm/types/exchange_test.go`
- `internal/avm/common/compiler/test.v.py`
- `internal/avm/common/compiler/test_bad.v.py`
- `internal/avm/common/mclock/mclock.s`
#### `internal/bundler` (8 files)

- `internal/bundler/bundle.go`
- `internal/bundler/config.go`
- `internal/bundler/mempool.go`
- `internal/bundler/metrics.go`
- `internal/bundler/service.go`
- `internal/bundler/validator.go`
- `internal/bundler/bundler_test.go`
- `internal/bundler/service_integration_test.go`
#### `internal/cache` (1 files)

- `internal/cache/lru.go`
#### `internal/consensus` (63 files)

- `internal/consensus/apoa/api.go`
- `internal/consensus/apoa/apoa.go`
- `internal/consensus/apoa/header_number.go`
- `internal/consensus/apoa/snapshot.go`
- `internal/consensus/apos/api.go`
- `internal/consensus/apos/apos.go`
- `internal/consensus/apos/consensus.go`
- `internal/consensus/apos/faker.go`
- `internal/consensus/apos/header_number.go`
- `internal/consensus/apos/pq_stark.go`
- `internal/consensus/apos/reward.go`
- `internal/consensus/apos/snapshot.go`
- `internal/consensus/base.go`
- `internal/consensus/common/constants.go`
- `internal/consensus/common/errors.go`
- `internal/consensus/consensus.go`
- `internal/consensus/engine.go`
- `internal/consensus/errors.go`
- `internal/consensus/hotstuff/adapter.go`
- `internal/consensus/hotstuff/bls_keystore.go`
- `internal/consensus/hotstuff/bls_util.go`
- `internal/consensus/hotstuff/codec.go`
- `internal/consensus/hotstuff/engine.go`
- `internal/consensus/hotstuff/errors.go`
- `internal/consensus/hotstuff/metrics.go`
- `internal/consensus/hotstuff/pacemaker.go`
- `internal/consensus/hotstuff/persistence.go`
- `internal/consensus/hotstuff/proposal.go`
- `internal/consensus/hotstuff/quorum.go`
- `internal/consensus/hotstuff/round_state.go`
- `internal/consensus/hotstuff/service.go`
- `internal/consensus/hotstuff/timeout.go`
- `internal/consensus/hotstuff/types.go`
- `internal/consensus/hotstuff/validator.go`
- `internal/consensus/hotstuff/voting.go`
- `internal/consensus/misc/constants.go`
- `internal/consensus/misc/dao.go`
- `internal/consensus/misc/difficulty.go`
- `internal/consensus/misc/eip1559.go`
- `internal/consensus/misc/eip4844.go`
- `internal/consensus/misc/errors.go`
- `internal/consensus/misc/gaslimit.go`
- `internal/consensus/misc/header.go`
- `internal/consensus/misc/seal.go`
- `internal/consensus/misc/secure_rand.go`
- `internal/consensus/apoa/api_test.go`
- `internal/consensus/apoa/apoa_test.go`
- `internal/consensus/apos/api_test.go`
- `internal/consensus/apos/apos_test.go`
- `internal/consensus/apos/pq_stark_test.go`
- `internal/consensus/apos/reward_test.go`
- `internal/consensus/base_test.go`
- `internal/consensus/consensus_test.go`
- `internal/consensus/engine_test.go`
- `internal/consensus/hotstuff/adapter_test.go`
- `internal/consensus/hotstuff/codec_test.go`
- `internal/consensus/hotstuff/hotstuff_test.go`
- `internal/consensus/hotstuff/persistence_test.go`
- `internal/consensus/misc/consensus_misc_test.go`
- `internal/consensus/misc/eip1559_test.go`
- `internal/consensus/misc/eip4844_test.go`
- `internal/consensus/misc/misc_test.go`
- `internal/consensus/misc/seal_test.go`
#### `internal/core` (2 files)

- `internal/core/container.go`
- `internal/core/container_test.go`
#### `internal/debug` (2 files)

- `internal/debug/api.go`
- `internal/debug/trace.go`
#### `internal/download` (11 files)

- `internal/download/block_number.go`
- `internal/download/dispatcher.go`
- `internal/download/download.go`
- `internal/download/fetchers.go`
- `internal/download/modes.go`
- `internal/download/peer.go`
- `internal/download/process.go`
- `internal/download/queue.go`
- `internal/download/response.go`
- `internal/download/resultstore.go`
- `internal/download/block_number_test.go`
#### `internal/exex` (4 files)

- `internal/exex/extensions/log_extension.go`
- `internal/exex/manager.go`
- `internal/exex/notification.go`
- `internal/exex/manager_test.go`
#### `internal/mcp` (5 files)

- `internal/mcp/block_number.go`
- `internal/mcp/resources.go`
- `internal/mcp/server.go`
- `internal/mcp/tools.go`
- `internal/mcp/tools_test.go`
#### `internal/metrics` (11 files)

- `internal/metrics/chain_metrics.go`
- `internal/metrics/log.go`
- `internal/metrics/prometheus/collector.go`
- `internal/metrics/prometheus/exp.go`
- `internal/metrics/prometheus/parseing.go`
- `internal/metrics/prometheus/prometheus.go`
- `internal/metrics/prometheus/register.go`
- `internal/metrics/prometheus/registry.go`
- `internal/metrics/prometheus/set.go`
- `internal/metrics/system_metrics.go`
- `internal/metrics/prometheus/prometheus_test.go`
#### `internal/mev` (3 files)

- `internal/mev/auction.go`
- `internal/mev/boost.go`
- `internal/mev/builder_api.go`
#### `internal/miner` (9 files)

- `internal/miner/block_number.go`
- `internal/miner/builder/bundle.go`
- `internal/miner/builder/ordering.go`
- `internal/miner/gas_limit.go`
- `internal/miner/miner.go`
- `internal/miner/worker.go`
- `internal/miner/builder/bundle_test.go`
- `internal/miner/builder/ordering_test.go`
- `internal/miner/miner_test.go`
#### `internal/network` (16 files)

- `internal/network/error.go`
- `internal/network/eth69/block_number.go`
- `internal/network/eth69/errors.go`
- `internal/network/eth69/handler.go`
- `internal/network/eth69/integration.go`
- `internal/network/eth69/peer_tracker.go`
- `internal/network/eth69/protocol.go`
- `internal/network/kad_dht.go`
- `internal/network/node.go`
- `internal/network/package.go`
- `internal/network/protocol.go`
- `internal/network/service.go`
- `internal/network/eth69/handler_test.go`
- `internal/network/package_test.go`
- `internal/network/service_test.go`
- `internal/network/eth69/README.md`
#### `internal/node` (17 files)

- `internal/node/adapters.go`
- `internal/node/block_number.go`
- `internal/node/devnet.go`
- `internal/node/endpoints.go`
- `internal/node/errors.go`
- `internal/node/health.go`
- `internal/node/history_expiry.go`
- `internal/node/jwt_handler.go`
- `internal/node/node.go`
- `internal/node/pruner.go`
- `internal/node/rpcstack.go`
- `internal/node/bundler_test.go`
- `internal/node/health_test.go`
- `internal/node/history_expiry_test.go`
- `internal/node/node_db_test.go`
- `internal/node/pruner_test.go`
- `internal/node/ratelimit_test.go`
#### `internal/p2p` (84 files)

- `internal/p2p/addr_factory.go`
- `internal/p2p/broadcaster.go`
- `internal/p2p/config.go`
- `internal/p2p/connection_gater.go`
- `internal/p2p/dial_relay_node.go`
- `internal/p2p/discover/common.go`
- `internal/p2p/discover/lookup.go`
- `internal/p2p/discover/metrics.go`
- `internal/p2p/discover/node.go`
- `internal/p2p/discover/ntp.go`
- `internal/p2p/discover/table.go`
- `internal/p2p/discover/v4_udp.go`
- `internal/p2p/discover/v4wire/v4wire.go`
- `internal/p2p/discover/v5_talk.go`
- `internal/p2p/discover/v5_udp.go`
- `internal/p2p/discover/v5wire/crypto.go`
- `internal/p2p/discover/v5wire/encoding.go`
- `internal/p2p/discover/v5wire/msg.go`
- `internal/p2p/discover/v5wire/pq_handshake.go`
- `internal/p2p/discover/v5wire/session.go`
- `internal/p2p/discovery.go`
- `internal/p2p/encoder/network_encoding.go`
- `internal/p2p/encoder/ssz.go`
- `internal/p2p/encoder/varint.go`
- `internal/p2p/enode/idscheme.go`
- `internal/p2p/enode/iter.go`
- `internal/p2p/enode/localnode.go`
- `internal/p2p/enode/node.go`
- `internal/p2p/enode/nodedb.go`
- `internal/p2p/enode/urlv4.go`
- `internal/p2p/enr/enr.go`
- `internal/p2p/enr/entries.go`
- `internal/p2p/fork.go`
- `internal/p2p/gossip_scoring_params.go`
- `internal/p2p/gossip_topic_mappings.go`
- `internal/p2p/handshake.go`
- `internal/p2p/interfaces.go`
- `internal/p2p/iterator.go`
- `internal/p2p/leakybucket/collector.go`
- `internal/p2p/leakybucket/heap.go`
- `internal/p2p/leakybucket/leakybucket.go`
- `internal/p2p/log.go`
- `internal/p2p/message_id.go`
- `internal/p2p/message_pool.go`
- `internal/p2p/monitoring.go`
- `internal/p2p/netutil/addrutil.go`
- `internal/p2p/netutil/error.go`
- `internal/p2p/netutil/iptrack.go`
- `internal/p2p/netutil/net.go`
- `internal/p2p/netutil/toobig_notwindows.go`
- `internal/p2p/netutil/toobig_windows.go`
- `internal/p2p/options.go`
- `internal/p2p/p2ptypes/rpc_errors.go`
- `internal/p2p/p2ptypes/rpc_goodbye_codes.go`
- `internal/p2p/p2ptypes/types.go`
- `internal/p2p/peers/peerdata/store.go`
- `internal/p2p/peers/scorers/bad_responses.go`
- `internal/p2p/peers/scorers/block_providers.go`
- `internal/p2p/peers/scorers/gossip_scorer.go`
- `internal/p2p/peers/scorers/peer_status.go`
- `internal/p2p/peers/scorers/service.go`
- `internal/p2p/peers/status.go`
- `internal/p2p/pubsub.go`
- `internal/p2p/pubsub_filter.go`
- `internal/p2p/pubsub_tracer.go`
- `internal/p2p/rpc_topic_mappings.go`
- `internal/p2p/sender.go`
- `internal/p2p/service.go`
- `internal/p2p/sync_interface.go`
- `internal/p2p/topics.go`
- `internal/p2p/utils.go`
- `internal/p2p/watch_peers.go`
- `internal/p2p/connection_gater_test.go`
- `internal/p2p/discover/v4wire/v4wire_test.go`
- `internal/p2p/discover/v5wire/crypto_test.go`
- `internal/p2p/discover/v5wire/encoding_test.go`
- `internal/p2p/discover/v5wire/pq_handshake_test.go`
- `internal/p2p/enode/localnode_test.go`
- `internal/p2p/enode/nodedb_test.go`
- `internal/p2p/enode/urlv4_test.go`
- `internal/p2p/enr/enr_test.go`
- `internal/p2p/gossip_topic_mappings_test.go`
- `internal/p2p/p2ptypes/types_test.go`
- `internal/p2p/sync_interface_test.go`
#### `internal/parallel` (10 files)

- `internal/parallel/executor.go`
- `internal/parallel/mvs.go`
- `internal/parallel/readwrite.go`
- `internal/parallel/scheduler.go`
- `internal/parallel/state_reader.go`
- `internal/parallel/state_writer.go`
- `internal/parallel/validator.go`
- `internal/parallel/executor_bench_test.go`
- `internal/parallel/executor_test.go`
- `internal/parallel/mvs_test.go`
#### `internal/peerdas` (8 files)

- `internal/peerdas/custody.go`
- `internal/peerdas/errors.go`
- `internal/peerdas/kzg.go`
- `internal/peerdas/producer.go`
- `internal/peerdas/service.go`
- `internal/peerdas/store.go`
- `internal/peerdas/types.go`
- `internal/peerdas/peerdas_test.go`
#### `internal/pubsub` (2 files)

- `internal/pubsub/pubsub.go`
- `internal/pubsub/pubsub_tracer.go`
#### `internal/snapshot` (7 files)

- `internal/snapshot/compress.go`
- `internal/snapshot/manager.go`
- `internal/snapshot/server.go`
- `internal/snapshot/types.go`
- `internal/snapshot/compress_test.go`
- `internal/snapshot/manager_test.go`
- `internal/snapshot/types_test.go`
#### `internal/sync` (65 files)

- `internal/sync/atomic_counter.go`
- `internal/sync/block_number.go`
- `internal/sync/checkpoint/service.go`
- `internal/sync/context.go`
- `internal/sync/deadlines.go`
- `internal/sync/decode_pubsub.go`
- `internal/sync/error.go`
- `internal/sync/fetcher.go`
- `internal/sync/initialsync/block_number.go`
- `internal/sync/initialsync/blocks_fetcher.go`
- `internal/sync/initialsync/blocks_fetcher_peers.go`
- `internal/sync/initialsync/blocks_fetcher_utils.go`
- `internal/sync/initialsync/blocks_queue.go`
- `internal/sync/initialsync/blocks_queue_utils.go`
- `internal/sync/initialsync/fsm.go`
- `internal/sync/initialsync/log.go`
- `internal/sync/initialsync/round_robin.go`
- `internal/sync/initialsync/service.go`
- `internal/sync/messagehandler.go`
- `internal/sync/metrics.go`
- `internal/sync/options.go`
- `internal/sync/rate_limiter.go`
- `internal/sync/rpc.go`
- `internal/sync/rpc_blob.go`
- `internal/sync/rpc_blocks_by_range.go`
- `internal/sync/rpc_chunked_response.go`
- `internal/sync/rpc_goodbye.go`
- `internal/sync/rpc_ping.go`
- `internal/sync/rpc_send_blob_request.go`
- `internal/sync/rpc_send_request.go`
- `internal/sync/rpc_send_snap_request.go`
- `internal/sync/rpc_snap.go`
- `internal/sync/rpc_snapshot.go`
- `internal/sync/rpc_status.go`
- `internal/sync/rpc_witness.go`
- `internal/sync/service.go`
- `internal/sync/sharded_map.go`
- `internal/sync/snapsync/block_number.go`
- `internal/sync/snapsync/manager.go`
- `internal/sync/snapsync/metrics.go`
- `internal/sync/snapsync/peer_scorer.go`
- `internal/sync/snapsync/progress.go`
- `internal/sync/snapsync/service.go`
- `internal/sync/snapsync/snapshot_client.go`
- `internal/sync/snapsync/task.go`
- `internal/sync/snapsync/validate.go`
- `internal/sync/snapsync/verify.go`
- `internal/sync/state_machine.go`
- `internal/sync/subscriber.go`
- `internal/sync/subscriber_blob.go`
- `internal/sync/subscriber_blocks.go`
- `internal/sync/subscription_topic_handler.go`
- `internal/sync/validate_blocks.go`
- `internal/sync/block_number_test.go`
- `internal/sync/checkpoint/service_test.go`
- `internal/sync/fetcher_test.go`
- `internal/sync/initialsync/block_number_test.go`
- `internal/sync/snapsync/peer_scorer_test.go`
- `internal/sync/snapsync/progress_test.go`
- `internal/sync/snapsync/snap_sync_test.go`
- `internal/sync/snapsync/snapshot_client_test.go`
- `internal/sync/snapsync/validate_test.go`
- `internal/sync/snapsync/verify_test.go`
- `internal/sync/state_machine_test.go`
- `internal/sync/sync_test.go`
#### `internal/tracers` (111 files)

- `internal/tracers/api.go`
- `internal/tracers/block_helpers.go`
- `internal/tracers/internal/tracetest/util.go`
- `internal/tracers/js/bigint.go`
- `internal/tracers/js/goja.go`
- `internal/tracers/js/internal/tracers/tracers.go`
- `internal/tracers/logger/access_list_tracer.go`
- `internal/tracers/logger/gen_structlog.go`
- `internal/tracers/logger/logger.go`
- `internal/tracers/logger/logger_json.go`
- `internal/tracers/native/4byte.go`
- `internal/tracers/native/call.go`
- `internal/tracers/native/call_flat.go`
- `internal/tracers/native/gen_account_json.go`
- `internal/tracers/native/gen_callframe_json.go`
- `internal/tracers/native/gen_flatcallaction_json.go`
- `internal/tracers/native/gen_flatcallresult_json.go`
- `internal/tracers/native/mux.go`
- `internal/tracers/native/noop.go`
- `internal/tracers/native/prestate.go`
- `internal/tracers/tracers.go`
- `internal/tracers/tracker.go`
- `internal/tracers/block_helpers_test.go`
- `internal/tracers/js/tracer_test.go`
- `internal/tracers/logger/logger_test.go`
- `internal/tracers/tracers_test.go`
- `internal/tracers/tracker_test.go`
- `internal/tracers/internal/tracetest/testdata/call_tracer/inner_throw_outer_revert.md`
- `internal/tracers/internal/tracetest/testdata/call_tracer/create.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/deep_calls.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/delegatecall.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/inner_create_oog_outer_throw.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/inner_instafail.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/inner_revert_reason.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/inner_throw_outer_revert.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/oog.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/revert.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/revert_reason.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/selfdestruct.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/simple.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/simple_onlytop.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer/throw.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/big_slow.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/callcode_precompiled_fail_hide.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/callcode_precompiled_oog.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/callcode_precompiled_throw.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/create.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/deep_calls.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/delegatecall.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/delegatecall_parent_value.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/gas.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/include_precompiled.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/inner_create_oog_outer_throw.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/inner_instafail.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/inner_precompiled_wrong_gas.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/inner_throw_outer_revert.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/nested_create.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/nested_create2_action_gas.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/nested_create_action_gas.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/nested_create_inerror.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/nested_pointer_issue.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/oog.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/option_convert_parity_errors.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/result_output.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/revert.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/revert_reason.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/selfdestruct.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/simple.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/skip_no_balance_error.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/staticcall_precompiled.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/suicide.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_flat/throw.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/create.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/deep_calls.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/delegatecall.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/inner_create_oog_outer_throw.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/inner_instafail.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/inner_throw_outer_revert.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/oog.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/revert.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/revert_reason.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/selfdestruct.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/simple.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_legacy/throw.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/calldata.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/delegatecall.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/multi_contracts.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/multilogs.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/notopic.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/simple.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/tx_failed.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/tx_partial_failed.json`
- `internal/tracers/internal/tracetest/testdata/call_tracer_withLog/with_onlyTopCall.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer/create_existing_contract.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer/simple.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer_legacy/simple.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer_with_diff_mode/create.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer_with_diff_mode/create_failed.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer_with_diff_mode/create_suicide.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer_with_diff_mode/inner_create.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer_with_diff_mode/simple.json`
- `internal/tracers/internal/tracetest/testdata/prestate_tracer_with_diff_mode/suicide.json`
- `internal/tracers/js/internal/tracers/4byte_tracer_legacy.js`
- `internal/tracers/js/internal/tracers/bigram_tracer.js`
- `internal/tracers/js/internal/tracers/call_tracer_legacy.js`
- `internal/tracers/js/internal/tracers/evmdis_tracer.js`
- `internal/tracers/js/internal/tracers/noop_tracer_legacy.js`
- `internal/tracers/js/internal/tracers/opcount_tracer.js`
- `internal/tracers/js/internal/tracers/prestate_tracer_legacy.js`
- `internal/tracers/js/internal/tracers/trigram_tracer.js`
- `internal/tracers/js/internal/tracers/unigram_tracer.js`
#### `internal/tracing` (3 files)

- `internal/tracing/exporter.go`
- `internal/tracing/tracing.go`
- `internal/tracing/tracing_test.go`
#### `internal/txgen` (1 files)

- `internal/txgen/txgen.go`
#### `internal/txspool` (21 files)

- `internal/txspool/block_number.go`
- `internal/txspool/dynamic_sizing.go`
- `internal/txspool/encrypted/decryptor.go`
- `internal/txspool/encrypted/encrypted_pool.go`
- `internal/txspool/encrypted/keyper.go`
- `internal/txspool/journal.go`
- `internal/txspool/read_state.go`
- `internal/txspool/tx_noncer.go`
- `internal/txspool/txs_fetcher.go`
- `internal/txspool/txs_list.go`
- `internal/txspool/txs_list_types.go`
- `internal/txspool/txs_pool.go`
- `internal/txspool/txs_pool_queues.go`
- `internal/txspool/txs_pool_types.go`
- `internal/txspool/block_number_test.go`
- `internal/txspool/dynamic_sizing_test.go`
- `internal/txspool/read_state_test.go`
- `internal/txspool/txs_fetcher_test.go`
- `internal/txspool/txs_list_test.go`
- `internal/txspool/txs_pool_validation_test.go`
- `internal/txspool/txspool_test.go`
#### `internal/vm` (126 files)

- `internal/vm/absint_cfg.go`
- `internal/vm/absint_cfg_proof_check.go`
- `internal/vm/absint_cfg_proof_gen.go`
- `internal/vm/analysis.go`
- `internal/vm/common.go`
- `internal/vm/contract.go`
- `internal/vm/contracts.go`
- `internal/vm/contracts_bls12381.go`
- `internal/vm/contracts_eip4844.go`
- `internal/vm/contracts_p256.go`
- `internal/vm/doc.go`
- `internal/vm/eips.go`
- `internal/vm/eips_amsterdam.go`
- `internal/vm/eips_cancun.go`
- `internal/vm/eips_fusaka.go`
- `internal/vm/eips_osaka.go`
- `internal/vm/eips_pectra.go`
- `internal/vm/eips_pectra_blob.go`
- `internal/vm/eips_prague.go`
- `internal/vm/eof.go`
- `internal/vm/erc4337.go`
- `internal/vm/errors.go`
- `internal/vm/evm.go`
- `internal/vm/evmtypes/evmtypes.go`
- `internal/vm/gas.go`
- `internal/vm/gas_table.go`
- `internal/vm/guest_apply.go`
- `internal/vm/instructions.go`
- `internal/vm/instrumented.go`
- `internal/vm/interface.go`
- `internal/vm/interpreter.go`
- `internal/vm/jump_table.go`
- `internal/vm/jump_table_cache.go`
- `internal/vm/logger.go`
- `internal/vm/memory.go`
- `internal/vm/memory_table.go`
- `internal/vm/mock_vm.go`
- `internal/vm/native_aa.go`
- `internal/vm/opcode_sizes.go`
- `internal/vm/opcodes.go`
- `internal/vm/operations_acl.go`
- `internal/vm/pool.go`
- `internal/vm/pq_contracts.go`
- `internal/vm/precompiles/contracts.go`
- `internal/vm/precompiles/registry.go`
- `internal/vm/precompiles_init.go`
- `internal/vm/runtime/doc.go`
- `internal/vm/runtime/env.go`
- `internal/vm/runtime/runtime.go`
- `internal/vm/safemath.go`
- `internal/vm/stack/stack.go`
- `internal/vm/stack_table.go`
- `internal/vm/contract_test.go`
- `internal/vm/contracts_fuzz_test.go`
- `internal/vm/contracts_modexp_test.go`
- `internal/vm/contracts_p256_test.go`
- `internal/vm/eips_cancun_test.go`
- `internal/vm/eips_fusaka_test.go`
- `internal/vm/eips_osaka_test.go`
- `internal/vm/eips_pectra_blob_test.go`
- `internal/vm/eips_pectra_revert_test.go`
- `internal/vm/eips_pectra_test.go`
- `internal/vm/eips_prague_test.go`
- `internal/vm/eof_execution_test.go`
- `internal/vm/eof_test.go`
- `internal/vm/erc4337_test.go`
- `internal/vm/errors_test.go`
- `internal/vm/guest_apply_test.go`
- `internal/vm/instructions_test.go`
- `internal/vm/interface_test.go`
- `internal/vm/interpreter_test.go`
- `internal/vm/memory_test.go`
- `internal/vm/opcodes_test.go`
- `internal/vm/pq_contracts_test.go`
- `internal/vm/precompiles/registry_test.go`
- `internal/vm/runtime/runtime_test.go`
- `internal/vm/safemath_test.go`
- `internal/vm/stack/stack_test.go`
- `internal/vm/vm_test.go`
- `internal/vm/testdata/precompiles/blake2F.json`
- `internal/vm/testdata/precompiles/blsG1Add.json`
- `internal/vm/testdata/precompiles/blsG1Mul.json`
- `internal/vm/testdata/precompiles/blsG1MultiExp.json`
- `internal/vm/testdata/precompiles/blsG2Add.json`
- `internal/vm/testdata/precompiles/blsG2Mul.json`
- `internal/vm/testdata/precompiles/blsG2MultiExp.json`
- `internal/vm/testdata/precompiles/blsMapG1.json`
- `internal/vm/testdata/precompiles/blsMapG2.json`
- `internal/vm/testdata/precompiles/blsPairing.json`
- `internal/vm/testdata/precompiles/bn256Add.json`
- `internal/vm/testdata/precompiles/bn256Pairing.json`
- `internal/vm/testdata/precompiles/bn256ScalarMul.json`
- `internal/vm/testdata/precompiles/ecRecover.json`
- `internal/vm/testdata/precompiles/fail-blake2f.json`
- `internal/vm/testdata/precompiles/fail-blsG1Add.json`
- `internal/vm/testdata/precompiles/fail-blsG1Mul.json`
- `internal/vm/testdata/precompiles/fail-blsG1MultiExp.json`
- `internal/vm/testdata/precompiles/fail-blsG2Add.json`
- `internal/vm/testdata/precompiles/fail-blsG2Mul.json`
- `internal/vm/testdata/precompiles/fail-blsG2MultiExp.json`
- `internal/vm/testdata/precompiles/fail-blsMapG1.json`
- `internal/vm/testdata/precompiles/fail-blsMapG2.json`
- `internal/vm/testdata/precompiles/fail-blsPairing.json`
- `internal/vm/testdata/precompiles/modexp.json`
- `internal/vm/testdata/precompiles/modexp_eip2565.json`
- `internal/vm/testdata/testcases_add.json`
- `internal/vm/testdata/testcases_and.json`
- `internal/vm/testdata/testcases_byte.json`
- `internal/vm/testdata/testcases_div.json`
- `internal/vm/testdata/testcases_eq.json`
- `internal/vm/testdata/testcases_exp.json`
- `internal/vm/testdata/testcases_gt.json`
- `internal/vm/testdata/testcases_lt.json`
- `internal/vm/testdata/testcases_mod.json`
- `internal/vm/testdata/testcases_mul.json`
- `internal/vm/testdata/testcases_or.json`
- `internal/vm/testdata/testcases_sar.json`
- `internal/vm/testdata/testcases_sdiv.json`
- `internal/vm/testdata/testcases_sgt.json`
- `internal/vm/testdata/testcases_shl.json`
- `internal/vm/testdata/testcases_shr.json`
- `internal/vm/testdata/testcases_signext.json`
- `internal/vm/testdata/testcases_slt.json`
- `internal/vm/testdata/testcases_smod.json`
- `internal/vm/testdata/testcases_sub.json`
- `internal/vm/testdata/testcases_xor.json`
#### `internal/zkprover` (15 files)

- `internal/zkprover/block_number.go`
- `internal/zkprover/guest/apply_tx.go`
- `internal/zkprover/guest/execute.go`
- `internal/zkprover/guest/types.go`
- `internal/zkprover/guest_types.go`
- `internal/zkprover/input_builder.go`
- `internal/zkprover/metrics.go`
- `internal/zkprover/proof.go`
- `internal/zkprover/service.go`
- `internal/zkprover/guest/apply_tx_test.go`
- `internal/zkprover/guest/execute_test.go`
- `internal/zkprover/guest_types_test.go`
- `internal/zkprover/input_builder_test.go`
- `internal/zkprover/proof_test.go`
- `internal/zkprover/service_test.go`
#### `internal/zkverifier` (2 files)

- `internal/zkverifier/verifier.go`
- `internal/zkverifier/verifier_test.go`
### 06. Shared State, DB, and RPC Modules (142 files)

#### `modules` (2 files)

- `modules/table.go`
- `modules/utils.go`
#### `modules/changeset` (4 files)

- `modules/changeset/account_changeset.go`
- `modules/changeset/changeset.go`
- `modules/changeset/storage_changeset.go`
- `modules/changeset/readme.md`
#### `modules/ethdb` (11 files)

- `modules/ethdb/bitmapdb/dbutils.go`
- `modules/ethdb/db_interface.go`
- `modules/ethdb/kv_util.go`
- `modules/ethdb/olddb/errors.go`
- `modules/ethdb/olddb/mapmutation.go`
- `modules/ethdb/olddb/mutation.go`
- `modules/ethdb/olddb/object_db.go`
- `modules/ethdb/olddb/reader.go`
- `modules/ethdb/olddb/tx_db.go`
- `modules/ethdb/walk.go`
- `modules/ethdb/olddb/olddb_test.go`
#### `modules/event` (5 files)

- `modules/event/v2/event.go`
- `modules/event/v2/feed.go`
- `modules/event/v2/subscription.go`
- `modules/event/v2/event_test.go`
- `modules/event/v2/subscription_test.go`
#### `modules/rawdb` (39 files)

- `modules/rawdb/accessors_account.go`
- `modules/rawdb/accessors_blob.go`
- `modules/rawdb/accessors_chain.go`
- `modules/rawdb/accessors_chain_blocks.go`
- `modules/rawdb/accessors_chain_head.go`
- `modules/rawdb/accessors_chain_receipts.go`
- `modules/rawdb/accessors_deposit.go`
- `modules/rawdb/accessors_history.go`
- `modules/rawdb/accessors_indexes.go`
- `modules/rawdb/accessors_metadata.go`
- `modules/rawdb/accessors_reward.go`
- `modules/rawdb/accessors_snapshot.go`
- `modules/rawdb/batch.go`
- `modules/rawdb/batch_read.go`
- `modules/rawdb/block_number.go`
- `modules/rawdb/bufpool.go`
- `modules/rawdb/freezer/ancient_reader.go`
- `modules/rawdb/freezer/freezer.go`
- `modules/rawdb/freezer/freezer_interface.go`
- `modules/rawdb/freezer/table.go`
- `modules/rawdb/freezer_integration.go`
- `modules/rawdb/interfaces.go`
- `modules/rawdb/lazy.go`
- `modules/rawdb/log_index.go`
- `modules/rawdb/log_index_read.go`
- `modules/rawdb/schema.go`
- `modules/rawdb/accessors_blob_test.go`
- `modules/rawdb/accessors_chain_test.go`
- `modules/rawdb/accessors_indexes_test.go`
- `modules/rawdb/accessors_reward_test.go`
- `modules/rawdb/accessors_test.go`
- `modules/rawdb/batch_read_test.go`
- `modules/rawdb/bench_test.go`
- `modules/rawdb/freezer/freezer_test.go`
- `modules/rawdb/interfaces_test.go`
- `modules/rawdb/lazy_bench_test.go`
- `modules/rawdb/lazy_test.go`
- `modules/rawdb/log_index_test.go`
- `modules/rawdb/schema_test.go`
#### `modules/rpc` (28 files)

- `modules/rpc/jsonrpc/client.go`
- `modules/rpc/jsonrpc/constants_unix.go`
- `modules/rpc/jsonrpc/constants_unix_nocgo.go`
- `modules/rpc/jsonrpc/endpoints.go`
- `modules/rpc/jsonrpc/errors.go`
- `modules/rpc/jsonrpc/handler.go`
- `modules/rpc/jsonrpc/http.go`
- `modules/rpc/jsonrpc/inproc.go`
- `modules/rpc/jsonrpc/ipc.go`
- `modules/rpc/jsonrpc/ipc_js.go`
- `modules/rpc/jsonrpc/ipc_unix.go`
- `modules/rpc/jsonrpc/ipc_windows.go`
- `modules/rpc/jsonrpc/json.go`
- `modules/rpc/jsonrpc/metrics.go`
- `modules/rpc/jsonrpc/ratelimit.go`
- `modules/rpc/jsonrpc/server.go`
- `modules/rpc/jsonrpc/service.go`
- `modules/rpc/jsonrpc/subscription.go`
- `modules/rpc/jsonrpc/toobig_notwindows.go`
- `modules/rpc/jsonrpc/toobig_windows.go`
- `modules/rpc/jsonrpc/types.go`
- `modules/rpc/jsonrpc/util.go`
- `modules/rpc/jsonrpc/websocket.go`
- `modules/rpc/jsonrpc/client_example_test.go`
- `modules/rpc/jsonrpc/client_subscribe_test.go`
- `modules/rpc/jsonrpc/client_test.go`
- `modules/rpc/jsonrpc/http_test.go`
- `modules/rpc/jsonrpc/json_parse_test.go`
#### `modules/state` (53 files)

- `modules/state/access_list.go`
- `modules/state/cached_state_reader.go`
- `modules/state/cached_state_writer.go`
- `modules/state/change_set_writer.go`
- `modules/state/commitment/account_encoding.go`
- `modules/state/commitment/jmt_commitment.go`
- `modules/state/commitment/key_hasher.go`
- `modules/state/commitment/root_computer.go`
- `modules/state/database.go`
- `modules/state/db_state_writer.go`
- `modules/state/entire.go`
- `modules/state/history.go`
- `modules/state/instrumented.go`
- `modules/state/interfaces.go`
- `modules/state/intra_block_state.go`
- `modules/state/journal.go`
- `modules/state/plain_readonly.go`
- `modules/state/plain_state_reader.go`
- `modules/state/plain_state_writer.go`
- `modules/state/pool.go`
- `modules/state/reader.go`
- `modules/state/snapshot/diff_collector.go`
- `modules/state/snapshot/diff_layer.go`
- `modules/state/snapshot/disk_layer.go`
- `modules/state/snapshot/generator.go`
- `modules/state/snapshot/journal.go`
- `modules/state/snapshot/metrics.go`
- `modules/state/snapshot/snapshot_reader.go`
- `modules/state/snapshot/tree.go`
- `modules/state/snapshot/types.go`
- `modules/state/snapshot/warmer.go`
- `modules/state/state_object.go`
- `modules/state/transient_storage.go`
- `modules/state/witness/block_verify.go`
- `modules/state/witness/encoding.go`
- `modules/state/witness/encoding_binary.go`
- `modules/state/witness/generator.go`
- `modules/state/witness/state_reader.go`
- `modules/state/witness/tracing_reader.go`
- `modules/state/witness/verify.go`
- `modules/state/witness/witness.go`
- `modules/state/cached_state_test.go`
- `modules/state/commitment/commitment_test.go`
- `modules/state/entire_test.go`
- `modules/state/instrumented_test.go`
- `modules/state/interfaces_test.go`
- `modules/state/intra_block_state_test.go`
- `modules/state/snapshot/persist_test.go`
- `modules/state/snapshot/snapshot_test.go`
- `modules/state/state_test.go`
- `modules/state/witness/encoding_binary_test.go`
- `modules/state/witness/integration_test.go`
- `modules/state/witness/verify_test.go`
### 07. Embedded Libraries and Externalized Subsystems (826 files)

#### `lib` (7 files)

- `lib/rules.go`
- `lib/tools.go`
- `lib/README.md`
- `lib/.gitignore`
- `lib/.golangci.yml`
- `lib/LICENSE`
- `lib/Makefile`
#### `lib/.github` (1 files)

- `lib/.github/workflows/ci.yml`
#### `lib/bptree` (11 files)

- `lib/bptree/binary_file.go`
- `lib/bptree/bulk.go`
- `lib/bptree/bulk_types.go`
- `lib/bptree/felt.go`
- `lib/bptree/graph.go`
- `lib/bptree/key_factory.go`
- `lib/bptree/node.go`
- `lib/bptree/tree.go`
- `lib/bptree/util.go`
- `lib/bptree/bulk_test.go`
- `lib/bptree/tree_test.go`
#### `lib/chain` (8 files)

- `lib/chain/aura_config.go`
- `lib/chain/chain_config.go`
- `lib/chain/chain_db.go`
- `lib/chain/consensus.go`
- `lib/chain/networkname/network_name.go`
- `lib/chain/snapcfg/util.go`
- `lib/chain/chain_config_test.go`
- `lib/chain/snapcfg/util_test.go`
#### `lib/commitment` (15 files)

- `lib/commitment/bin_patricia_hashed.go`
- `lib/commitment/bin_patricia_hashed_hash.go`
- `lib/commitment/bin_patricia_hashed_ops.go`
- `lib/commitment/bin_patricia_hashed_types.go`
- `lib/commitment/commitment.go`
- `lib/commitment/hex_patricia_hashed.go`
- `lib/commitment/hex_patricia_hashed_hash.go`
- `lib/commitment/hex_patricia_hashed_ops.go`
- `lib/commitment/hex_patricia_hashed_types.go`
- `lib/commitment/bin_patricia_hashed_test.go`
- `lib/commitment/commitment_test.go`
- `lib/commitment/hex_patricia_hashed_bench_test.go`
- `lib/commitment/hex_patricia_hashed_fuzz_test.go`
- `lib/commitment/hex_patricia_hashed_test.go`
- `lib/commitment/patricia_state_mock_test.go`
#### `lib/common` (64 files)

- `lib/common/address.go`
- `lib/common/assert/assert_disable.go`
- `lib/common/assert/assert_enable.go`
- `lib/common/background/progress.go`
- `lib/common/big.go`
- `lib/common/bitutil/select.go`
- `lib/common/bytes.go`
- `lib/common/bytes4.go`
- `lib/common/bytes48.go`
- `lib/common/bytes64.go`
- `lib/common/bytes96.go`
- `lib/common/chan.go`
- `lib/common/cli.go`
- `lib/common/cmp/cmp.go`
- `lib/common/collections.go`
- `lib/common/concurrent/concurrent.go`
- `lib/common/copybytes.go`
- `lib/common/datadir/dirs.go`
- `lib/common/dbg/dbg_ctx.go`
- `lib/common/dbg/dbg_env.go`
- `lib/common/dbg/experiments.go`
- `lib/common/dbg/leak_detector.go`
- `lib/common/dbg/log_panic.go`
- `lib/common/dir/rw_dir.go`
- `lib/common/dir/rw_dir_generic.go`
- `lib/common/dir/rw_dir_windows.go`
- `lib/common/disk/common.go`
- `lib/common/disk/disk.go`
- `lib/common/disk/disk_darwin.go`
- `lib/common/disk/disk_linux.go`
- `lib/common/eth.go`
- `lib/common/eth2shuffle/shuffle.go`
- `lib/common/fixedgas/protocol.go`
- `lib/common/hash.go`
- `lib/common/hasher.go`
- `lib/common/hextobytes.go`
- `lib/common/hexutil/hexutil.go`
- `lib/common/hexutil/json.go`
- `lib/common/hexutility/bytes.go`
- `lib/common/hexutility/errors.go`
- `lib/common/hexutility/hex.go`
- `lib/common/hexutility/json.go`
- `lib/common/hexutility/text.go`
- `lib/common/length/length.go`
- `lib/common/math/big.go`
- `lib/common/math/integer.go`
- `lib/common/math/modexp.go`
- `lib/common/mem/common.go`
- `lib/common/mem/mem.go`
- `lib/common/mem/mem_linux.go`
- `lib/common/metrics/block_metrics.go`
- `lib/common/metrics/metrics_enabled.go`
- `lib/common/ring/ring.go`
- `lib/common/sorted.go`
- `lib/common/u256/big.go`
- `lib/common/eth2shuffle/shuffle_bench_test.go`
- `lib/common/eth2shuffle/shuffle_test.go`
- `lib/common/hexutil/hexutil_test.go`
- `lib/common/hexutil/json_test.go`
- `lib/common/hexutility/hex_test.go`
- `lib/common/math/big_test.go`
- `lib/common/math/integer_test.go`
- `lib/common/sorted_test.go`
- `lib/common/eth2shuffle/spec/tests.csv`
#### `lib/config3` (4 files)

- `lib/config3/config3.go`
- `lib/config3/erigon3_test_disable.go`
- `lib/config3/erigon3_test_enable.go`
- `lib/config3/erigon4_test_enable.go`
#### `lib/crypto` (64 files)

- `lib/crypto/blake2b/blake2b.go`
- `lib/crypto/blake2b/blake2b_amd64.go`
- `lib/crypto/blake2b/blake2b_avx2_amd64.go`
- `lib/crypto/blake2b/blake2b_f_fuzz.go`
- `lib/crypto/blake2b/blake2b_generic.go`
- `lib/crypto/blake2b/blake2b_ref.go`
- `lib/crypto/blake2b/blake2x.go`
- `lib/crypto/blake2b/register.go`
- `lib/crypto/bn256/bn256_fast.go`
- `lib/crypto/bn256/bn256_slow.go`
- `lib/crypto/bn256/cloudflare/bn256.go`
- `lib/crypto/bn256/cloudflare/constants.go`
- `lib/crypto/bn256/cloudflare/curve.go`
- `lib/crypto/bn256/cloudflare/gfp.go`
- `lib/crypto/bn256/cloudflare/gfp12.go`
- `lib/crypto/bn256/cloudflare/gfp2.go`
- `lib/crypto/bn256/cloudflare/gfp6.go`
- `lib/crypto/bn256/cloudflare/gfp_decl.go`
- `lib/crypto/bn256/cloudflare/gfp_generic.go`
- `lib/crypto/bn256/cloudflare/lattice.go`
- `lib/crypto/bn256/cloudflare/optate.go`
- `lib/crypto/bn256/cloudflare/twist.go`
- `lib/crypto/bn256/google/bn256.go`
- `lib/crypto/bn256/google/constants.go`
- `lib/crypto/bn256/google/curve.go`
- `lib/crypto/bn256/google/gfp12.go`
- `lib/crypto/bn256/google/gfp2.go`
- `lib/crypto/bn256/google/gfp6.go`
- `lib/crypto/bn256/google/optate.go`
- `lib/crypto/bn256/google/twist.go`
- `lib/crypto/crypto.go`
- `lib/crypto/cryptopool/pool.go`
- `lib/crypto/ecies/ecies.go`
- `lib/crypto/ecies/params.go`
- `lib/crypto/kzg/kzg.go`
- `lib/crypto/secp256r1/publickey.go`
- `lib/crypto/secp256r1/verifier.go`
- `lib/crypto/signature_cgo.go`
- `lib/crypto/signature_nocgo.go`
- `lib/crypto/blake2b/blake2b_f_test.go`
- `lib/crypto/blake2b/blake2b_test.go`
- `lib/crypto/bn256/cloudflare/bn256_test.go`
- `lib/crypto/bn256/cloudflare/example_test.go`
- `lib/crypto/bn256/cloudflare/gfp_test.go`
- `lib/crypto/bn256/cloudflare/lattice_test.go`
- `lib/crypto/bn256/cloudflare/main_test.go`
- `lib/crypto/bn256/google/bn256_test.go`
- `lib/crypto/bn256/google/example_test.go`
- `lib/crypto/bn256/google/main_test.go`
- `lib/crypto/crypto_test.go`
- `lib/crypto/ecies/ecies_test.go`
- `lib/crypto/signature_test.go`
- `lib/crypto/blake2b/blake2bAVX2_amd64.s`
- `lib/crypto/blake2b/blake2b_amd64.s`
- `lib/crypto/bn256/LICENSE`
- `lib/crypto/bn256/cloudflare/LICENSE`
- `lib/crypto/bn256/cloudflare/gfp_amd64.s`
- `lib/crypto/bn256/cloudflare/gfp_arm64.s`
- `lib/crypto/bn256/cloudflare/mul_amd64.h`
- `lib/crypto/bn256/cloudflare/mul_arm64.h`
- `lib/crypto/bn256/cloudflare/mul_bmi2_amd64.h`
- `lib/crypto/ecies/.gitignore`
- `lib/crypto/ecies/LICENSE`
- `lib/crypto/ecies/README`
#### `lib/diagnostics` (20 files)

- `lib/diagnostics/block_execution.go`
- `lib/diagnostics/bodies.go`
- `lib/diagnostics/client.go`
- `lib/diagnostics/entities.go`
- `lib/diagnostics/headers.go`
- `lib/diagnostics/network.go`
- `lib/diagnostics/provider.go`
- `lib/diagnostics/resources_usage.go`
- `lib/diagnostics/snapshots.go`
- `lib/diagnostics/snapshots_download.go`
- `lib/diagnostics/snapshots_indexing.go`
- `lib/diagnostics/speedtest.go`
- `lib/diagnostics/stages.go`
- `lib/diagnostics/sys_info.go`
- `lib/diagnostics/utils.go`
- `lib/diagnostics/network_test.go`
- `lib/diagnostics/provider_test.go`
- `lib/diagnostics/snapshots_test.go`
- `lib/diagnostics/stages_test.go`
- `lib/diagnostics/utils_test.go`
#### `lib/direct` (11 files)

- `lib/direct/downloader_client.go`
- `lib/direct/eth_backend_client.go`
- `lib/direct/execution_client.go`
- `lib/direct/mining_client.go`
- `lib/direct/sentinel_client.go`
- `lib/direct/sentry_client.go`
- `lib/direct/sentry_client_mock.go`
- `lib/direct/sentry_client_mock_stream.go`
- `lib/direct/sentry_client_stream.go`
- `lib/direct/state_diff_client.go`
- `lib/direct/txpool_client.go`
#### `lib/diskutils` (2 files)

- `lib/diskutils/diskutils.go`
- `lib/diskutils/diskutils_darwin.go`
#### `lib/downloader` (35 files)

- `lib/downloader/database.go`
- `lib/downloader/downloader.go`
- `lib/downloader/downloader_grpc_server.go`
- `lib/downloader/downloadercfg/dns_cache_resolver.go`
- `lib/downloader/downloadercfg/downloadercfg.go`
- `lib/downloader/downloadercfg/logger.go`
- `lib/downloader/downloadercfg/logger_libutp.go`
- `lib/downloader/downloadergrpc/client.go`
- `lib/downloader/file_hash.go`
- `lib/downloader/http.go`
- `lib/downloader/mainloop.go`
- `lib/downloader/mdbx_piece_completion.go`
- `lib/downloader/path.go`
- `lib/downloader/path_plan9.go`
- `lib/downloader/path_unix.go`
- `lib/downloader/path_windows.go`
- `lib/downloader/rclone.go`
- `lib/downloader/rclone_config.go`
- `lib/downloader/snapshot_lock.go`
- `lib/downloader/snaptype/caplin_types.go`
- `lib/downloader/snaptype/files.go`
- `lib/downloader/snaptype/snaptypes.go`
- `lib/downloader/snaptype/type.go`
- `lib/downloader/stats.go`
- `lib/downloader/torrent.go`
- `lib/downloader/torrent_files.go`
- `lib/downloader/util.go`
- `lib/downloader/web_download.go`
- `lib/downloader/webseed.go`
- `lib/downloader/downloader_test.go`
- `lib/downloader/mdbx_piece_completion_test.go`
- `lib/downloader/rclone_test.go`
- `lib/downloader/snaptype/caplin_types_test.go`
- `lib/downloader/README.md`
- `lib/downloader/components.png`
#### `lib/etl` (10 files)

- `lib/etl/buffers.go`
- `lib/etl/collector.go`
- `lib/etl/dataprovider.go`
- `lib/etl/etl.go`
- `lib/etl/heap.go`
- `lib/etl/progress.go`
- `lib/etl/etl_test.go`
- `lib/etl/README.md`
- `lib/etl/ETL-collector.png`
- `lib/etl/ETL.png`
#### `lib/gointerfaces` (28 files)

- `lib/gointerfaces/downloader/downloader.pb.go`
- `lib/gointerfaces/downloader/downloader_grpc.pb.go`
- `lib/gointerfaces/execution/execution.pb.go`
- `lib/gointerfaces/execution/execution_grpc.pb.go`
- `lib/gointerfaces/grpcutil/utils.go`
- `lib/gointerfaces/remote/ethbackend.pb.go`
- `lib/gointerfaces/remote/ethbackend_grpc.pb.go`
- `lib/gointerfaces/remote/kv.pb.go`
- `lib/gointerfaces/remote/kv_client_mock.go`
- `lib/gointerfaces/remote/kv_grpc.pb.go`
- `lib/gointerfaces/remote/kv_state_changes_client_mock.go`
- `lib/gointerfaces/remote/mockgen.go`
- `lib/gointerfaces/remote/sort.go`
- `lib/gointerfaces/sentinel/sentinel.pb.go`
- `lib/gointerfaces/sentinel/sentinel_grpc.pb.go`
- `lib/gointerfaces/sentry/mockgen.go`
- `lib/gointerfaces/sentry/sentry.pb.go`
- `lib/gointerfaces/sentry/sentry_client_mock.go`
- `lib/gointerfaces/sentry/sentry_grpc.pb.go`
- `lib/gointerfaces/sentry/sentry_server_mock.go`
- `lib/gointerfaces/txpool/mining.pb.go`
- `lib/gointerfaces/txpool/mining_grpc.pb.go`
- `lib/gointerfaces/txpool/txpool.pb.go`
- `lib/gointerfaces/txpool/txpool_grpc.pb.go`
- `lib/gointerfaces/type_utils.go`
- `lib/gointerfaces/types/types.pb.go`
- `lib/gointerfaces/version.go`
- `lib/gointerfaces/remote/sort_test.go`
#### `lib/jmt` (13 files)

- `lib/jmt/batch.go`
- `lib/jmt/errors.go`
- `lib/jmt/hasher.go`
- `lib/jmt/nibble.go`
- `lib/jmt/node.go`
- `lib/jmt/proof.go`
- `lib/jmt/store.go`
- `lib/jmt/store/lazy_db_store.go`
- `lib/jmt/store/mdbx_store.go`
- `lib/jmt/store/memstore.go`
- `lib/jmt/store/store.go`
- `lib/jmt/tree.go`
- `lib/jmt/tree_test.go`
#### `lib/kv` (70 files)

- `lib/kv/backup/backup.go`
- `lib/kv/bitmapdb/bitmapdb.go`
- `lib/kv/bitmapdb/stream.go`
- `lib/kv/dbutils/composite_keys.go`
- `lib/kv/dbutils/helper.go`
- `lib/kv/dbutils/history_index.go`
- `lib/kv/dbutils/suffix_type.go`
- `lib/kv/helpers.go`
- `lib/kv/iter/helpers.go`
- `lib/kv/iter/iter.go`
- `lib/kv/iter/iter_interface.go`
- `lib/kv/kv_interface.go`
- `lib/kv/kvcache/cache.go`
- `lib/kv/kvcache/dummy.go`
- `lib/kv/kvcfg/accessors_config.go`
- `lib/kv/layered/cache.go`
- `lib/kv/layered/config.go`
- `lib/kv/layered/extract.go`
- `lib/kv/layered/layered_db.go`
- `lib/kv/layered/layered_tx.go`
- `lib/kv/layered/metrics.go`
- `lib/kv/layered/tables.go`
- `lib/kv/mdbx/kv_mdbx.go`
- `lib/kv/mdbx/kv_mdbx_batch.go`
- `lib/kv/mdbx/kv_mdbx_bucket.go`
- `lib/kv/mdbx/kv_mdbx_cursor.go`
- `lib/kv/mdbx/kv_mdbx_dupsort.go`
- `lib/kv/mdbx/kv_mdbx_iterator.go`
- `lib/kv/mdbx/kv_mdbx_opts.go`
- `lib/kv/mdbx/kv_mdbx_temporary.go`
- `lib/kv/mdbx/kv_mdbx_tx.go`
- `lib/kv/mdbx/kv_mdbx_tx_commit.go`
- `lib/kv/mdbx/util.go`
- `lib/kv/membatch/mapmutation.go`
- `lib/kv/membatchwithdb/memory_mutation.go`
- `lib/kv/membatchwithdb/memory_mutation_cursor.go`
- `lib/kv/membatchwithdb/memory_mutation_diff.go`
- `lib/kv/memdb/memory_database.go`
- `lib/kv/order/order.go`
- `lib/kv/rawdbv3/txnum.go`
- `lib/kv/remotedb/kv_remote.go`
- `lib/kv/remotedbserver/remotedbserver.go`
- `lib/kv/remotedbserver/snapshots_mock.go`
- `lib/kv/tables.go`
- `lib/kv/temporal/historyv2/account_changeset.go`
- `lib/kv/temporal/historyv2/changeset.go`
- `lib/kv/temporal/historyv2/find_by_history.go`
- `lib/kv/temporal/historyv2/storage_changeset.go`
- `lib/kv/temporal/kv_temporal.go`
- `lib/kv/temporal/temporaltest/kv_temporal_testdb.go`
- `lib/kv/bitmapdb/bitmapdb_test.go`
- `lib/kv/dbutils/composite_keys_test.go`
- `lib/kv/iter/iter_test.go`
- `lib/kv/kvcache/cache_test.go`
- `lib/kv/layered/bench_test.go`
- `lib/kv/layered/cache_test.go`
- `lib/kv/layered/layered_db_test.go`
- `lib/kv/mdbx/kv_abstract_test.go`
- `lib/kv/mdbx/kv_mdbx_cursor_test.go`
- `lib/kv/mdbx/kv_mdbx_test.go`
- `lib/kv/mdbx/kv_migrator_test.go`
- `lib/kv/membatch/database_test.go`
- `lib/kv/membatch/mapmutation_test.go`
- `lib/kv/membatchwithdb/memory_mutation_test.go`
- `lib/kv/remotedb/kv_remote_test.go`
- `lib/kv/remotedbserver/remotedbserver_test.go`
- `lib/kv/temporal/historyv2/account_changeset_test.go`
- `lib/kv/temporal/kv_temporal_test.go`
- `lib/kv/Readme.md`
- `lib/kv/temporal/historyv2/readme.md`
#### `lib/log` (25 files)

- `lib/log/skip.go`
- `lib/log/v3/doc.go`
- `lib/log/v3/ext/handler.go`
- `lib/log/v3/ext/id.go`
- `lib/log/v3/format.go`
- `lib/log/v3/handler.go`
- `lib/log/v3/logger.go`
- `lib/log/v3/root.go`
- `lib/log/v3/syslog.go`
- `lib/log/v3/term/doc.go`
- `lib/log/v3/term/terminal_appengine.go`
- `lib/log/v3/term/terminal_darwin.go`
- `lib/log/v3/term/terminal_freebsd.go`
- `lib/log/v3/term/terminal_linux.go`
- `lib/log/v3/term/terminal_netbsd.go`
- `lib/log/v3/term/terminal_notwindows.go`
- `lib/log/v3/term/terminal_openbsd.go`
- `lib/log/v3/term/terminal_solaris.go`
- `lib/log/v3/term/terminal_windows.go`
- `lib/log/v3/bench_test.go`
- `lib/log/v3/ext/ext_test.go`
- `lib/log/v3/log_test.go`
- `lib/log/v3/logger_test.go`
- `lib/log/LICENSE`
- `lib/log/v3/term/LICENSE`
#### `lib/metrics` (11 files)

- `lib/metrics/counter.go`
- `lib/metrics/duration_observer.go`
- `lib/metrics/gauge.go`
- `lib/metrics/histogram.go`
- `lib/metrics/parsing.go`
- `lib/metrics/register.go`
- `lib/metrics/set.go`
- `lib/metrics/setup.go`
- `lib/metrics/summary.go`
- `lib/metrics/timer.go`
- `lib/metrics/value_getter.go`
#### `lib/mmap` (5 files)

- `lib/mmap/mmap_unix.go`
- `lib/mmap/mmap_windows.go`
- `lib/mmap/total_memory.go`
- `lib/mmap/total_memory_cgroups.go`
- `lib/mmap/total_memory_cgroups_stub.go`
#### `lib/pedersen_hash` (25 files)

- `lib/pedersen_hash/hash.go`
- `lib/pedersen_hash/README.md`
- `lib/pedersen_hash/LICENSE`
- `lib/pedersen_hash/big_int.h`
- `lib/pedersen_hash/big_int.inl`
- `lib/pedersen_hash/elliptic_curve.h`
- `lib/pedersen_hash/elliptic_curve.inl`
- `lib/pedersen_hash/elliptic_curve_constants.cc`
- `lib/pedersen_hash/elliptic_curve_constants.h`
- `lib/pedersen_hash/error_handling.h`
- `lib/pedersen_hash/ffi_pedersen_hash.cc`
- `lib/pedersen_hash/ffi_pedersen_hash.h`
- `lib/pedersen_hash/ffi_utils.cc`
- `lib/pedersen_hash/ffi_utils.h`
- `lib/pedersen_hash/fraction_field_element.h`
- `lib/pedersen_hash/fraction_field_element.inl`
- `lib/pedersen_hash/gsl-lite.hpp`
- `lib/pedersen_hash/hash.cc`
- `lib/pedersen_hash/hash.h`
- `lib/pedersen_hash/math.h`
- `lib/pedersen_hash/pedersen_hash.cc`
- `lib/pedersen_hash/pedersen_hash.h`
- `lib/pedersen_hash/prime_field_element.cc`
- `lib/pedersen_hash/prime_field_element.h`
- `lib/pedersen_hash/prng.h`
#### `lib/recsplit` (39 files)

- `lib/recsplit/eliasfano16/elias_fano.go`
- `lib/recsplit/eliasfano32/elias_fano.go`
- `lib/recsplit/golomb_rice.go`
- `lib/recsplit/index.go`
- `lib/recsplit/index_reader.go`
- `lib/recsplit/recsplit.go`
- `lib/recsplit/eliasfano16/elias_fano_fuzz_test.go`
- `lib/recsplit/eliasfano32/elias_fano_fuzz_test.go`
- `lib/recsplit/eliasfano32/elias_fano_test.go`
- `lib/recsplit/index_test.go`
- `lib/recsplit/recsplit_fuzz_test.go`
- `lib/recsplit/recsplit_test.go`
- `lib/recsplit/.gitignore`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzDoubleEliasFano/17e481a7c1425c40f663d83515ab93ee97d7108181870a3747d4aeca7fbb2648`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzDoubleEliasFano/1a646c505776a883b2d99ecc5e83f54a70b9cbac79cdad92901d202e481461ae`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzDoubleEliasFano/1af797790141e786f451a1d4d47f37452233883d41160cfbadc06e2bfcf17ae9`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzDoubleEliasFano/5199aaf4a8e7ccb61efaa0a3fc90ecd4d142bce89a912fb84536632b1277a760`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzDoubleEliasFano/a07f63d0e074619c4fe923533ea5c72af1c00e2aff3206f345b9767ee9ce4101`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzDoubleEliasFano/b7ae575f1e43328af34baad9490d5639f50d6afda42ef20438d6a1d4a0e5a88e`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzSingleEliasFano/4ed490ae7dc318c0525e1e514cec72681ec2e72ffb9e5571d1c31ee26cb94a73`
- `lib/recsplit/eliasfano16/testdata/fuzz/FuzzSingleEliasFano/fb292a3777de8fcb809bf1d7bf13bffc3c2b7d8b1df25511af87e0872cebe3c7`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzDoubleEliasFano/17e481a7c1425c40f663d83515ab93ee97d7108181870a3747d4aeca7fbb2648`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzDoubleEliasFano/1a646c505776a883b2d99ecc5e83f54a70b9cbac79cdad92901d202e481461ae`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzDoubleEliasFano/1af797790141e786f451a1d4d47f37452233883d41160cfbadc06e2bfcf17ae9`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzDoubleEliasFano/5199aaf4a8e7ccb61efaa0a3fc90ecd4d142bce89a912fb84536632b1277a760`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzDoubleEliasFano/a07f63d0e074619c4fe923533ea5c72af1c00e2aff3206f345b9767ee9ce4101`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzDoubleEliasFano/b7ae575f1e43328af34baad9490d5639f50d6afda42ef20438d6a1d4a0e5a88e`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzSingleEliasFano/4ed490ae7dc318c0525e1e514cec72681ec2e72ffb9e5571d1c31ee26cb94a73`
- `lib/recsplit/eliasfano32/testdata/fuzz/FuzzSingleEliasFano/fb292a3777de8fcb809bf1d7bf13bffc3c2b7d8b1df25511af87e0872cebe3c7`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/0bb14f20865563b5504c292a005834e5e04d6094622a40844dffedb78e560eab`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/13f42b07eca1d28428c3ea36e8ec409764afc9351e3f09e4d91b80626067ea59`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/363f36b97269af400b867a8b03e9eff1eeedb2ceb2dfe516a4cef4a74b309b5e`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/38b6ae40b3e89854b01ee0627bdb24c634f32809c12ddca378e1d61c617d9649`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/61bad6c11050935c60bf7f0d15e40fbb20ec1a70dab26f62bf92a49706920440`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/87f7c74ee952d2ef8af8df250b939c4a65677eff54de2393c8f2b896e250813f`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/8dcbe8c6685bcbfb81a3a3e5e8eb005af3edb0f0bf2f653f0430942379c90e7c`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/93906988de1687555e538207931e6022243d7a38d6b2926e04c866dbb8318d54`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/a62376aebd0437e22ed6eace28704d5225ae77b615952d99e85accd632d416d2`
- `lib/recsplit/testdata/fuzz/FuzzRecSplit/dc722115a839e9b801755d0efbe86e6d9c9199e2ec36d0a4ee4f67f31aab1519`
#### `lib/rlp` (17 files)

- `lib/rlp/commitment.go`
- `lib/rlp/decode.go`
- `lib/rlp/doc.go`
- `lib/rlp/encode.go`
- `lib/rlp/encode_rlp2.go`
- `lib/rlp/iterator.go`
- `lib/rlp/parse.go`
- `lib/rlp/raw.go`
- `lib/rlp/typecache.go`
- `lib/rlp/decode_tail_test.go`
- `lib/rlp/decode_test.go`
- `lib/rlp/encode_test.go`
- `lib/rlp/encoder_example_test.go`
- `lib/rlp/iterator_test.go`
- `lib/rlp/parse_test.go`
- `lib/rlp/raw_test.go`
- `lib/rlp/rlp_fuzz_test.go`
#### `lib/rlp2` (11 files)

- `lib/rlp2/commitment.go`
- `lib/rlp2/decoder.go`
- `lib/rlp2/encodel.go`
- `lib/rlp2/encoder.go`
- `lib/rlp2/parse.go`
- `lib/rlp2/types.go`
- `lib/rlp2/unmarshaler.go`
- `lib/rlp2/util.go`
- `lib/rlp2/parse_test.go`
- `lib/rlp2/unmarshaler_test.go`
- `lib/rlp2/readme.md`
#### `lib/seg` (30 files)

- `lib/seg/compress.go`
- `lib/seg/compress_dict.go`
- `lib/seg/decompress.go`
- `lib/seg/decompress_dict.go`
- `lib/seg/decompress_reader.go`
- `lib/seg/parallel_compress.go`
- `lib/seg/patricia/patricia.go`
- `lib/seg/patricia/patricia_types.go`
- `lib/seg/sais/sais.go`
- `lib/seg/compress_fuzz_test.go`
- `lib/seg/compress_test.go`
- `lib/seg/decompress_bench_test.go`
- `lib/seg/decompress_fuzz_test.go`
- `lib/seg/decompress_test.go`
- `lib/seg/patricia/patricia_fuzz_test.go`
- `lib/seg/patricia/patricia_test.go`
- `lib/seg/sais/sais_test.go`
- `lib/seg/silkworm_seg_fuzz_test.go`
- `lib/seg/sais/README.md`
- `lib/seg/patricia/testdata/fuzz/FuzzLongestMatch/3a5198b65396851670329467bf211856973858cf006ef30532d6871ea859a12a`
- `lib/seg/patricia/testdata/fuzz/FuzzLongestMatch/50e6d6e88241b5d113eeb578e3f53211f9d4c2605391a92b5314b1522ddd6613`
- `lib/seg/patricia/testdata/fuzz/FuzzLongestMatch/a6e7cfd5b704609ef4eae0891c8bd6f60cfbe3da1bf98f71ce0c3e107042154e`
- `lib/seg/patricia/testdata/fuzz/FuzzLongestMatch/eae7318dcf13903566ac6ce58a3188dd26cc3216cdb8a4c398871feb71d79749`
- `lib/seg/patricia/testdata/fuzz/FuzzPatricia/1ac0f70817537550272339767003fa71f827da8ab9b1466b539a97b48b0bec89`
- `lib/seg/patricia/testdata/fuzz/FuzzPatricia/77fc7eba78cd0b1fa2a157aa2cc7e164eed8ca2c71f13d4e103e5a76887a341b`
- `lib/seg/patricia/testdata/fuzz/FuzzPatricia/82c51172146d16d565cd6de38398aba6284e6acc17a97edccb0be3a97624f967`
- `lib/seg/sais/sais.c`
- `lib/seg/sais/sais.h`
- `lib/seg/sais/utils.c`
- `lib/seg/sais/utils.h`
#### `lib/state` (33 files)

- `lib/state/aggregator.go`
- `lib/state/aggregator_files.go`
- `lib/state/aggregator_rotx.go`
- `lib/state/btree_alloc.go`
- `lib/state/btree_index.go`
- `lib/state/domain.go`
- `lib/state/domain_committed.go`
- `lib/state/domain_rotx.go`
- `lib/state/domain_shared.go`
- `lib/state/files_item.go`
- `lib/state/history.go`
- `lib/state/history_asof_iter.go`
- `lib/state/history_build.go`
- `lib/state/history_changes_iter.go`
- `lib/state/history_maintenance.go`
- `lib/state/history_reader.go`
- `lib/state/history_step.go`
- `lib/state/history_wal.go`
- `lib/state/inverted_index.go`
- `lib/state/inverted_index_rotx.go`
- `lib/state/merge.go`
- `lib/state/state_recon.go`
- `lib/state/txnum_guard.go`
- `lib/state/aggregator_bench_test.go`
- `lib/state/aggregator_fuzz_test.go`
- `lib/state/aggregator_test.go`
- `lib/state/domain_test.go`
- `lib/state/gc_test.go`
- `lib/state/history_iter_test.go`
- `lib/state/history_reader_test.go`
- `lib/state/history_test.go`
- `lib/state/inverted_index_test.go`
- `lib/state/merge_test.go`
#### `lib/sysutils` (2 files)

- `lib/sysutils/sysutils.go`
- `lib/sysutils/sysutils_test.go`
#### `lib/tools` (3 files)

- `lib/tools/golangci_lint.sh`
- `lib/tools/licenses_check.sh`
- `lib/tools/mod_tidy_check.sh`
#### `lib/txpool` (245 files)

- `lib/txpool/announcements.go`
- `lib/txpool/auth.go`
- `lib/txpool/blobs.go`
- `lib/txpool/by_nonce.go`
- `lib/txpool/fetch.go`
- `lib/txpool/lifecycle.go`
- `lib/txpool/mainloop.go`
- `lib/txpool/persistence.go`
- `lib/txpool/pool.go`
- `lib/txpool/pool_mock.go`
- `lib/txpool/pool_types.go`
- `lib/txpool/selection.go`
- `lib/txpool/send.go`
- `lib/txpool/senders.go`
- `lib/txpool/subpools.go`
- `lib/txpool/test_util.go`
- `lib/txpool/testdata.go`
- `lib/txpool/txpool_grpc_server.go`
- `lib/txpool/txpoolcfg/txpoolcfg.go`
- `lib/txpool/txpoolutil/all_components.go`
- `lib/txpool/validation.go`
- `lib/txpool/fetch_test.go`
- `lib/txpool/pool_fuzz_test.go`
- `lib/txpool/pool_test.go`
- `lib/txpool/txpoolcfg/txpoolcfg_test.go`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/031b5468dd4ee8aa`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0575660696010bb14ad331b850e3d4d1f4b30c8c4e735815bd1b6fa338397d8e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/08cd725229a87596`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0a8b1f449653d512ac00e798566a2da9679092432eb7bea67396b4f080069e67`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0ac54290179bf6ef`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0c41865a11bcac8f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0c661f76a0c6b791ed4fb2d7f60f6da6706a2c25e81302f8f4fef62733689d2e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0d69569d199ef72dbbfe082414ec505212f8ad0dbf751c1ecc1d6c088ba99aa0`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0dea9613d08d0546`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0f23e70134217855`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/0f336f154886e0e569dda236c4bb6a35d4cd52a48764ee57bb7a91be67a4a2fa`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/10d1b8428204585c77904cca3e21f23183b2f4b9c1c07e101bb45e79dde1dc18`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/11069168737a2a9e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1149d4a5fc6b1b33ceeb225b3499b827cf2cb689aaad2b72f29501784d8b467f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/132b2d4bc4fd7fc58b57d1bb5e52f2dac12d351ca8c44f9bb552dbfb62376648`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/13a0e557328e6bf3f795dcd6e8af1771b69a9f454c87ec44d3f32e3053368237`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/14a21bc246dcb474`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/14cab1d854913a30`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/16402edb3d27369e47b23367304fca2018bccc5a4f23d5fa55bc7b274912848f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1768f5f2354f556a1fcd1517427a39a317db5d59e62243cd5e7d313db42bafb4`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/183413db397e4b3eb89459522129442c90ef0eb2beb4c7e2ff0f5337d928a22d`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1a0acf41b03a59b9a18da3bfad33d5f4d2bc3a96977d967e0cf6912904bdf8fa`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1a5ad15fc9bbdd6a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1cd2ee51303a010a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1d3bc8cea4840be26f5a92e7966d41a5e3cd0407c19a97e7369e56112011fd0f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1d918c65115fa74b`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/1db35749fc80a92c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/20e871ae7f07881f9658636c8c510bd3b6f30344bce11296d558a375d075c640`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/2180ddbb5cd3a283d898339db926fb4914d519ba4f13c1626c0897ff51b01912`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/21e90bfa162081777c615b93d26fa0a842b47e0bba32106faa664a6cdd910e2f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/2232e00f4cf291e5`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/2517166c5a21ae56`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/258cb72390f12d44`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/277a8dd40c96504eca5d82227b879aad0e9a58b66bd3e642a8fc9badd57e753c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/27c07831038bb604`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/283aecbf9ad942b1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/293335c4369a7812`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/2cf3e4bb7c12d79f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/2ec0e55367a622ff`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/2f93157de97b7de6`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3107ce1575f9f732`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/31a6943437a2267a8aa5b948d8fcb84951c43d6a01ee607c1fbb4c2e08445aa6`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3227a680e8c45d97`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3261c7e8b18a8ad2`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/335eb6018c909912`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/36812261c46c7ee3`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/37b5245d92542265`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/392c1b3fea5f7c25`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3a39e33504867a3b`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3c5ce2b7a8ca79ec`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3d83e32c8b42dd084f7a2ea07e0354626615a3bb369f900db1a49ff9b99d46b1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3f33fcca3c967e9d1a1ccfc53dfddf5e4ff3bc49fbc0de0d3c9343fc7679f6e2`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/3f9a8e8b240af40ef9c1a5634cc12437b8a2e7aeae427d56bc47fd2c10e01061`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/4195fee9ba765a9d2fdce0821cd3719033a6cc7ad2551fb0e2d0c1fecfc6cc59`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/425d6c98562f0b61`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/440ad72e9cc066215453870075777f4fbdee9222976dd926390656efd7bfa32b`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/4415f5762aa84c6fce4ae46fedfb5bf373ace5caccaea33376f3ec804ea52cd4`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/482961ec519214d30d06694b020fb6a794cdb5861a3337e64433209f4751bb58`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/4a65116dd6e8a601`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/4df776d6b0542639`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/4f5fc32b64c4e0c3`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/4fab3f9fb0d8320f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/4fe0cb3bd36e5426`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/511b6e9fb88f5aa460b33143b7860b1f6bab2c39ecb56e6f937504929edfeed6`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/53872473537386d04aceb53085c4e9460b4d1570159608e3a6f1035caee05de4`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/54129e4ff403946ef1453fcc3694543d3de1e934ea8ee0ba5d9fe189efc46db6`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/54c829cb66f8bf6a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/5551744a9729a660`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/570d4a0a58f06c78b6d3dab98eb83075bc07cefe48037535ac7e9eaffde6b5bd`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/57cd4868aa9f53f8a13ded7080da45c1dad94fb91f9dd605a9f2778659395965`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/592958e76ba4c4b8`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/59c2366b6c454e0f0285cde4e3417ff2212a9e98c859505eb2b6e465271aac77`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/5ab37706d578b70e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/5e28788089091e34`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/5f60c893359960df`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/60221d93724986a9af6290b616aac2f19e86fb49f2cc6e52d1eda17a83d75e7d`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/611f922a438ff4d2`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/612e7b2496b80c05704feacd832c816a6be66d5d9d2f23e42ddf65dbd367e6db`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/636b2af939e9f493bbc3d01676d8e68e217221114ffeab890af2fe1e98b868a7`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/6553f3b16e413698`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/67c54eb87af25be1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/69e5878d85648ade`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/69e7bce1a7fb58439308891c9e98001988490ff60aeb9b9243d8332802173fef`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/6cd4e3d0f1b84c2beae785ac28a840e2a3ac1f359f83df0c0cbf49c998d7b07b`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/6d1f2cfa77856642374f73cd7c80ff6cf9aad7deb9965cccfe0fa49eff8406d4`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/6d8fe0cadac03533`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/6e0755fb78f94582`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/6f74df7b7889b3a6`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/6fad5d3460432a6c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/7014865c34218763`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/70fab60190356106fd342ef842d24c3d1e92189286b6d6210b131ff7a760feeb`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/71d08575bac1187a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/74019f4cf0b1e4f0`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/74364dc0d7b55cdb`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/7481c7f5446e09681b2db138cc7cbcda0d4d0f960e3e8c6a551a3c85e445c6c5`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/760a70c9812dbebe`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/7625027b6ab0d751`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/77225716368e90b097c8cf2b789f4f039d6c398f1ae4487dd0b713a716318b08`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/7a38294a110c1b9a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/7c42ba72f5d2ec31bac5aca511a40b1fe59af096fa104f50c1e14e94462ebd2e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/7d43ba109342836fb454ec50bda61903156547e90512cbd6810950f97452e9d3`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/80a7cfc969f02f43`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8266009464e80359`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/83051cafaef01d551779c76d074c5be01eea5da36cf554be801fbf1f64b92171`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8643665bb19a4e64a92412fdbb624789a40309e7fc761df2b497b1dda7c9340e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/86eab8f298e0d934`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8745e7699e04335442f4c64b1c36045d966e372213cc9015c9418271cf334a42`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/87d5115d5cf06bea0b5878b0ea065409be4663e4ae060b414a9a567d5af8d1a3`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8a792d25488fb8d9c56ab4c8ac4110735807bc7f54c4ef29aadd976937d40325`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8bb44fe3de88aa52`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8c4ccbd55eb40e3c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8cba2922ddf362c4`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8ccc94a4891b102d`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8d534a36ee4867cf`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/8f4a9335b20a4c90ae5c90ba97c907eeeae7071e227c4eabb94aacd4d0c55aab`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/938fdcb295d9272a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/93a268ec92f31ad5`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9426411803b6686c713892091ab7233a3dd83a8ad6d14cc4529daf4dff96eb9e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/952fd56b52f5bcf8`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/95fea26f1ec6e657`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/96210a12cf076cd6`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/984d3b671a7cd2c96b4af770c9539022b0c86db6380ed4d30816a0eff88609c2`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/986769c9616764b29befa62e07dd2d69c4486cf649c448ca60cb1118b047e4ec`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9a8c922f8273f70c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9afc3e2938c7d658`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9b53b7328856e794`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9c43daf54dff2ee6`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9c6f74e3c4d4bb36`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9e070c3535760ef1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/9e1195c23477b728`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a0dd6b3db9136eec33f441194cbfc480b8f716cf9b9ba145620c6dac7a5eebc1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a105bfe19478b90f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a372d6fb3cd244bf`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a3c5b8c990919bd9`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a40b66f7d13c72f9`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a4e7a9fbd99ef2be`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a8e21c52d77a4f7f4f77135a518476a8947b0c7675ea458dc0c912533de9fd43`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/a9e56991344685d83bc24b83d4cf6a22c6b6d55d50a96eeaab6adca19fc84f38`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/aa8dd4d5e1cd3d7c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/af0cff582dea0a45`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b25164ebd6e73e27`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b2a9891c4bef161b3d1d3b580d2e6b92a52bafb48612f6e60fdca171e1dd2edb`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b2ec4f4ac33c6140`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b48eeae2d3e0ad5a5b8312b402cf17cc73863ffd1b96bb446bbe6665849ea403`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b588596fe5fecb2a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b5cd7077a07b79ab`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b6b579c95a9f2032`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b86a252eec71c70037eb21f9df61718d0d817fbf207d911a3754bd0e97f7afe8`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b8b7fcb7e40cdc42942be76e828c692d1d61cfca15d86f3d520379a33f67a767`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/b91a7a020d7503d751f03ec88810602e75e4e2c816977b85ffc32e7cee51fff1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/bc6aed70917e0ad389786593534d68699bbf040e8a81cae987c5a39269de1437`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/bc6d4b77ba5ae31d7cae96bf145cb3baf72ae9b3b6aebe798ae96061f221355c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/bc70f430feaeef2c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/beedaf68a441c47d54d538f0ed089be88b83cd5e7644d9e43be76dce5df05104`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/bff70686510c5cced0cb2db9f9242b6776fe84b88279970ccdd0a624c144c61e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c0526c4ad2d93dbe9a9aa04aeec9f8e7b57d72b3f95cb630ee45499500f2b6c3`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c0b4e99f15ba7fac`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c12326ae90a8f4e745c92e49d678d9b91b67bf57263d7c43ceba1b1a67eb67a1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c17cc1da48f7d408`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c24a1a6f986841fa`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c32ec3b62a3829bf`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c5f67b9db9785b095cba597383ba6c5d452b32e8f1c760ebf7ff24d99d20c27c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/c8f39cdb47e0e791c39b54720de0ef50d41fcc0e94e0a68bea360a5ba3c1afee`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/ca3f9f1a99647d26`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/cb2b3e19a54f7b1148a2c8c46550b00c4323c25bdbb73983a9c954594084e6d8`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/cd232ca4732cee3912581ab2b60cab102b92d5de1624e8f3ae7afaa05eafe7ec`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/cf2ad641c6d15aaf`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d04bfeab54c0f1a2c645005dc14bfd6c7b4c47f3eb105e8b43d4a0fee256c47b`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d172fff99c82ebb42fcdc45cbbcaf5046c9ff3da3f5fa4b42c89dcc1ecd90a42`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d1803357dbaac00a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d2af8bc122cb6223`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d31defd969621711`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d3a4ac3ba0f78575d887fd4081b9db065fd6e438d016aeed8241eca8efaf7986`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d545a1f08f6c8304744182c12491dce389aa094eb7ba0c26189b5e11092ff4bf`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d68db16d56595759`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d840212e7358e2558eafb1f52ad3020067c83196950237ea0a450f9af1e2fc90`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d97d641a7d126aff1bf7be2fd7a8c033118400950372d459cf3abd72bd3d567e`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d9a79eaef0920c969764a6530cbd97a5713c166105c09e3fbfa591b1924e3abc`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/d9b61f439ffc2bebcbebdac0064fdceec1378a1bdf22ec3acbd0a88c428e9e52`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/db08e1d09af84a94`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/db4436256e1918dc`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/ddee2f30fd0ced2d`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e116bec86aad6799`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e1cdb71537fc53949dd34d1102edb4b726de54ba3c14fa5923a41499f6d43231`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e1d7591c7410c871499be14484e259354d7154dbeaa4d2451c6fdbd75376de2f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e2c3ac1d8b3a2c62f0c4546d0a1a86483073205855e6b20719629bc1d3aaa1b5`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e313080d525daa83`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e3a5234dfe52cf5d`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e46e3202b5de2064`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e99d98a7070b9740`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/e9ff902cd55a14b6fcc788771976e9e0e9705dca0e1e97383c40ec19c5f025b0`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/ed1187e2b11d2e03`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/efa4981c756bc5ae3db729125786da45aa29da53e2b617918357b21969dc1d65`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f060280a7ec0cc24d368c664a8f5a9f11cd7b2ec20f1d1a617b44c7bec81efea`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f0f172c1b84cede1588c50a7581c4f26e733fd9aaa1234c23049ebd1f7fedcf5`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f26c7ba7a79565e9`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f2bf2a5224eb4c93`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f2e46905ce6c482a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f46052c7735aaa57136d3ef415c203061ee1c5fc2120c3755e8b20cf38f040d1`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f4b4021a5d03f4ee254eed62271d8816352eabb127e7a96c00f955656143d56a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f510e7a10865786c46481205fe7373cf6681a07b884ee932ea645c501a9d9b2a`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f6b86e85dba81b71`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/f94a9848151c073c`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/fa2ba582a32f3c4d`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/fa73cf98e372e378981b8aec0926ae20f036bac5f7ebc1ce94811e88bfb05ff7`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/fc249f6a6ead5679f61b6f95377742005c33c7a95241a82125bc3362c5eff390`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/fcf37437789ba29f`
- `lib/txpool/testdata/fuzz/FuzzOnNewBlocks/ff70b90eaacc561089848e559d1b8029559bdbf75ddeec8543369d7c976f24f1`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/0bb14f20865563b5504c292a005834e5e04d6094622a40844dffedb78e560eab`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/13f42b07eca1d28428c3ea36e8ec409764afc9351e3f09e4d91b80626067ea59`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/363f36b97269af400b867a8b03e9eff1eeedb2ceb2dfe516a4cef4a74b309b5e`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/38b6ae40b3e89854b01ee0627bdb24c634f32809c12ddca378e1d61c617d9649`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/61bad6c11050935c60bf7f0d15e40fbb20ec1a70dab26f62bf92a49706920440`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/863f4df5673bd59166d2194b83ba85150963f17e0cfd89e44eb5129cb0d514ec`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/87f7c74ee952d2ef8af8df250b939c4a65677eff54de2393c8f2b896e250813f`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/8dcbe8c6685bcbfb81a3a3e5e8eb005af3edb0f0bf2f653f0430942379c90e7c`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/93906988de1687555e538207931e6022243d7a38d6b2926e04c866dbb8318d54`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/a62376aebd0437e22ed6eace28704d5225ae77b615952d99e85accd632d416d2`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/c5fe4a7d971f6b57feba5390d5b03015c577f6a930d1a45bda4a5eb69db9dd5d`
- `lib/txpool/testdata/recsplit/FuzzRecSplit/dc722115a839e9b801755d0efbe86e6d9c9199e2ec36d0a4ee4f67f31aab1519`
#### `lib/types` (16 files)

- `lib/types/clonable/clonable.go`
- `lib/types/ssz/errors.go`
- `lib/types/ssz/ssz.go`
- `lib/types/testdata.go`
- `lib/types/txn.go`
- `lib/types/txn_codec.go`
- `lib/types/txn_packets.go`
- `lib/types/txn_types.go`
- `lib/types/txn_packets_fuzz_test.go`
- `lib/types/txn_packets_test.go`
- `lib/types/txn_test.go`
- `lib/types/txn_types_fuzz_test.go`
- `lib/types/testdata/fuzz/FuzzGetPooledTransactions66/35ee0280203b07f8c31d8fe5709ceebe1cc40a6a58ceafb2c03cb61f382ba47b`
- `lib/types/testdata/fuzz/FuzzPooledTransactions66/495796522cf9f672fef390129494776063de46bedc6461b3fde47a3fed63701f`
- `lib/types/testdata/fuzz/FuzzPooledTransactions66/8d3f97ae581a3b81e90f7bbcdfef335dbcb36807a6efc576624e518f9fb7e929`
- `lib/types/testdata/fuzz/FuzzPooledTransactions66/c6652a57f98dde9e244c20ff8fb4bd1b2e347a597f2d3e2a71edc4118d4e1ce5`
#### `lib/wrap` (1 files)

- `lib/wrap/e3_wrapper.go`
### 08. Smart Contracts and Generated Bindings (20 files)

#### `contracts/deposit` (16 files)

- `contracts/deposit/amt/deposit.go`
- `contracts/deposit/contract.go`
- `contracts/deposit/fuji/deposit.go`
- `contracts/deposit/nft/NFT.go`
- `contracts/deposit/contract_test.go`
- `contracts/deposit/amt/Deposit.sol`
- `contracts/deposit/amt/IDeposit.sol`
- `contracts/deposit/amt/abi.json`
- `contracts/deposit/amt/bytecode.bin`
- `contracts/deposit/fuji/IDeposit.sol`
- `contracts/deposit/fuji/abi.json`
- `contracts/deposit/fuji/staking.sol`
- `contracts/deposit/nft/IDeposit.sol`
- `contracts/deposit/nft/abi.json`
- `contracts/deposit/nft/bytecode.bin`
- `contracts/deposit/nft/staking.sol`
#### `contracts/pqregistry` (4 files)

- `contracts/pqregistry/registry.go`
- `contracts/pqregistry/registry_test.go`
- `contracts/pqregistry/IPQKeyRegistry.sol`
- `contracts/pqregistry/PQKeyRegistry.sol`
### 09. Tests, Benchmarks, and Research (41 files)

#### `root` (2 files)

- `tps_benchmark_results.txt`
- `tpsbench`
#### `tests` (26 files)

- `tests/memory_state.go`
- `tests/analyze_failures_test.go`
- `tests/bls_precompile_test.go`
- `tests/dapp_compat_phase2_test.go`
- `tests/dapp_compat_phase3_test.go`
- `tests/dapp_compat_phase4_test.go`
- `tests/dapp_compat_test.go`
- `tests/eth_state_test.go`
- `tests/eth_test_runner_test.go`
- `tests/full_state_test.go`
- `tests/integration_test.go`
- `tests/pq_integration_test.go`
- `tests/prediction_market_compat_test.go`
- `tests/refactoring_test.go`
- `tests/state_test.go`
- `tests/zk_evm_compat_test.go`
- `tests/COMPLETE_FIX_SUMMARY.md`
- `tests/CURRENT_TEST_STATUS.md`
- `tests/EIP1559_FAILURE_ANALYSIS.md`
- `tests/EIP7623_FIX_SUMMARY.md`
- `tests/FIX_PROGRESS_Jan2026.md`
- `tests/TEST_IMPROVEMENTS.md`
- `tests/TEST_RESULTS_v5.4.0.md`
- `tests/TEST_STATUS.md`
- `tests/TEST_STATUS_FINAL.md`
- `tests/test_report.txt`
#### `tests/allocs` (1 files)

- `tests/allocs/amc.json`
#### `tools/bench` (6 files)

- `tools/bench/cmd/metrics/main.go`
- `tools/bench/cmd/rpc/main.go`
- `tools/bench/tps/cmd/main.go`
- `tools/bench/tps/tps_bench.go`
- `tools/bench/README.md`
- `tools/bench/run_smoke.sh`
#### `tools/tpsbench` (3 files)

- `tools/tpsbench/tps_bench.go`
- `tools/tpsbench/tps_bench_test.go`
- `tools/tpsbench/README.md`
#### `turbo` (3 files)

- `turbo/backup/backup.go`
- `turbo/rpchelper/helper.go`
- `turbo/rpchelper/rpc_block.go`
### 10. Operations, Tooling, and Documentation (96 files)

#### `deployments/explorer` (1 files)

- `deployments/explorer/config.env`
#### `deployments/influxdb` (2 files)

- `deployments/influxdb/config.yml`
- `deployments/influxdb/influx-configs`
#### `deployments/prometheus` (7 files)

- `deployments/prometheus/dashboards/amc.json`
- `deployments/prometheus/dashboards/amc_internal.json`
- `deployments/prometheus/dashboards/dashboard.yml`
- `deployments/prometheus/dashboards/n42_advanced.json`
- `deployments/prometheus/datasources/prometheus.yml`
- `deployments/prometheus/grafana.ini`
- `deployments/prometheus/prometheus.yml`
#### `docs` (7 files)

- `docs/ARCHITECTURE.md`
- `docs/CHANGELOG.md`
- `docs/DEVLOG.md`
- `docs/GAP_ANALYSIS.md`
- `docs/QUICKSTART.md`
- `docs/SUMMARY.md`
- `docs/intro.md`
#### `docs/cli` (1 files)

- `docs/cli/cli.md`
#### `docs/developers` (4 files)

- `docs/developers/codeofconduct.md`
- `docs/developers/codesubmission.md`
- `docs/developers/contribute.md`
- `docs/developers/developers.md`
#### `docs/engineering` (15 files)

- `docs/engineering/CODE_REVIEW_FILE_INVENTORY.md`
- `docs/engineering/CODE_REVIEW_FINDINGS.md`
- `docs/engineering/CODE_REVIEW_MASTER_PLAN.md`
- `docs/engineering/ETH69_IMPLEMENTATION.md`
- `docs/engineering/ETH_EL_TEST_PLAN.md`
- `docs/engineering/METRICS_BASELINE.md`
- `docs/engineering/PERFORMANCE_OPTIMIZATION_PLAN.md`
- `docs/engineering/PERFORMANCE_OPTIMIZATION_REPORT.md`
- `docs/engineering/POST_QUANTUM_UPGRADE_PLAN.md`
- `docs/engineering/PREDICTION_MARKET_GUIDE.md`
- `docs/engineering/REFACTORING_BLUEPRINT.md`
- `docs/engineering/REFACTORING_CHANGELOG.md`
- `docs/engineering/SECURITY_AUDIT_REPORT.md`
- `docs/engineering/TEST_PLAN.md`
- `docs/engineering/VM_UPDATE_GUIDE.md`
#### `docs/installation` (5 files)

- `docs/installation/binaries.md`
- `docs/installation/build-for-arm-devices.md`
- `docs/installation/docker.md`
- `docs/installation/installation.md`
- `docs/installation/source.md`
#### `docs/integration` (2 files)

- `docs/integration/BLOCKSCOUT_V9.3.2_COMPATIBILITY.md`
- `docs/integration/BLOCKSCOUT_V9.3.2_IMPLEMENTATION.md`
#### `docs/jsonrpc` (14 files)

- `docs/jsonrpc/admin.md`
- `docs/jsonrpc/consensus_beacon_ext.md`
- `docs/jsonrpc/debug.md`
- `docs/jsonrpc/eth.md`
- `docs/jsonrpc/flashbots.md`
- `docs/jsonrpc/intro.md`
- `docs/jsonrpc/miner.md`
- `docs/jsonrpc/net.md`
- `docs/jsonrpc/otterscan.md`
- `docs/jsonrpc/reth.md`
- `docs/jsonrpc/rpc.md`
- `docs/jsonrpc/trace.md`
- `docs/jsonrpc/txpool.md`
- `docs/jsonrpc/web3.md`
#### `docs/run` (7 files)

- `docs/run/mainnet.md`
- `docs/run/observability.md`
- `docs/run/ports.md`
- `docs/run/private-testnet.md`
- `docs/run/run-a-node.md`
- `docs/run/transactions.md`
- `docs/run/troubleshooting.md`
#### `log` (4 files)

- `log/logrus.go`
- `log/pretty_formatter.go`
- `log/root.go`
- `log/root_test.go`
#### `log/logrus-prefixed-formatter` (6 files)

- `log/logrus-prefixed-formatter/formatter.go`
- `log/logrus-prefixed-formatter/formatter_test.go`
- `log/logrus-prefixed-formatter/logrus_prefixed_formatter_suite_test.go`
- `log/logrus-prefixed-formatter/README.md`
- `log/logrus-prefixed-formatter/BUILD.bazel`
- `log/logrus-prefixed-formatter/LICENSE`
#### `pkg` (2 files)

- `pkg/errors/errors.go`
- `pkg/errors/errors_test.go`
#### `scripts` (7 files)

- `scripts/bump_version.sh`
- `scripts/generate_code_review_inventory.sh`
- `scripts/log_monitor.sh`
- `scripts/stress_test.sh`
- `scripts/test_blockscout.sh`
- `scripts/test_rpc.sh`
- `scripts/tx_load_generator.py`
#### `utils` (12 files)

- `utils/async.go`
- `utils/bytes.go`
- `utils/crypto.go`
- `utils/forks.go`
- `utils/lock.go`
- `utils/network.go`
- `utils/safego.go`
- `utils/util.go`
- `utils/async_test.go`
- `utils/safego_test.go`
- `utils/util_test.go`
- `utils/utils_extra_test.go`
### 11. Unclassified (2 files)

#### `root` (1 files)

- `docker-compose.yml`
#### `sdk` (1 files)

- `sdk/sdk.go`
