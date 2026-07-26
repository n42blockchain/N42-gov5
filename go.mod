module github.com/n42blockchain/N42

go 1.25.7

require (
	github.com/RoaringBitmap/roaring v1.9.4
	github.com/VictoriaMetrics/metrics v1.44.0
	github.com/btcsuite/btcd/btcec/v2 v2.5.0
	github.com/c2h5oh/datasize v0.0.0-20231215233829-aa82cc1e6500
	github.com/cespare/cp v1.1.1
	github.com/consensys/gnark-crypto v0.20.1
	github.com/crate-crypto/go-kzg-4844 v1.1.0
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc
	github.com/deckarep/golang-set v1.8.0
	github.com/deckarep/golang-set/v2 v2.9.0
	github.com/dop251/goja v0.0.0-20260701091749-b07b74453ea9
	github.com/erigontech/mdbx-go v0.40.3
	github.com/ethereum/go-ethereum v1.17.4
	github.com/fjl/gencodec v0.1.2
	github.com/fsnotify/fsnotify v1.10.1
	github.com/go-kit/kit v0.13.0
	github.com/go-stack/stack v1.8.1
	github.com/gofrs/flock v0.13.0
	github.com/golang-jwt/jwt/v4 v4.5.2
	github.com/golang/snappy v1.0.0
	github.com/google/btree v1.1.3
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/hashicorp/go-bexpr v0.1.16
	github.com/hashicorp/golang-lru v1.0.2
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/holiman/bloomfilter/v2 v2.0.3
	github.com/holiman/uint256 v1.3.2 // resolved via replace below to v1.2.3 to preserve current MarshalJSON/MainnetGenesisHash semantics
	github.com/kr/pretty v0.3.1
	github.com/libp2p/go-libp2p v0.48.0
	github.com/libp2p/go-libp2p-kad-dht v0.41.0
	github.com/libp2p/go-libp2p-pubsub v0.17.0
	github.com/mgutz/ansi v0.0.0-20200706080929-d51e80ef957d
	github.com/multiformats/go-multiaddr v0.16.1
	github.com/onsi/ginkgo v1.16.5
	github.com/onsi/gomega v1.42.1
	github.com/paulbellamy/ratecounter v0.2.0
	github.com/peterh/liner v1.2.2
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.2
	github.com/prometheus/common v0.70.1
	github.com/prysmaticlabs/fastssz v0.0.0-20260421202104-7a6eb71e6e45
	github.com/rcrowley/go-metrics v0.0.0-20250401214520-65e299d6c5c9
	github.com/rs/cors v1.11.1
	github.com/sirupsen/logrus v1.9.4
	github.com/stretchr/testify v1.11.1
	github.com/supranational/blst v0.3.16
	github.com/trailofbits/go-mutexasserts v0.0.0-20250514102930-c1f3d2e37561
	github.com/urfave/cli/v2 v2.27.7
	go.opencensus.io v0.24.0
	go.uber.org/zap v1.28.0
	golang.org/x/crypto v0.54.0
	golang.org/x/exp v0.0.0-20260718201538-764159d718ef
	golang.org/x/sync v0.22.0
	golang.org/x/sys v0.47.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gopkg.in/natefinch/npipe.v2 v2.0.0-20160621034901-c1b8fa8bdcce
	gopkg.in/yaml.v2 v2.4.0
)

replace github.com/holiman/uint256 => github.com/holiman/uint256 v1.2.3

require (
	github.com/anacrolix/torrent v1.61.0
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/containerd/cgroups/v3 v3.1.3
	github.com/crate-crypto/go-eth-kzg v1.5.0
	github.com/crate-crypto/go-ipa v0.0.0-20240724233137-53bbb0ceb27a
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1
	github.com/edsrzf/mmap-go v1.2.0
	github.com/erigontech/fastkeccak v0.1.0
	github.com/erigontech/interfaces v0.0.0-20260309190044-b1ca32817912
	github.com/erigontech/secp256k1 v1.2.0
	github.com/erigontech/speedtest v0.0.2
	github.com/ethereum/go-verkle v0.2.2
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0
	github.com/klauspost/compress v1.19.1
	github.com/mattn/go-colorable v0.1.15
	github.com/mattn/go-isatty v0.0.24
	github.com/pbnjay/memory v0.0.0-20210728143218-7b4eea64cf58
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/pion/webrtc/v4 v4.2.16
	github.com/prysmaticlabs/gohashtree v0.0.5-beta
	github.com/puzpuzpuz/xsync/v4 v4.5.0
	github.com/quasilyte/go-ruleguard/dsl v0.3.23
	github.com/shirou/gopsutil/v4 v4.26.6
	github.com/spaolacci/murmur3 v1.1.0
	github.com/spf13/afero v1.15.0
	github.com/tetratelabs/wazero v1.12.0
	github.com/thomaso-mirodin/intmath v0.0.0-20160323211736-5dc6d854e46e
	github.com/tidwall/btree v1.8.1
	github.com/ugorji/go/codec v1.3.1
	go.opentelemetry.io/otel v1.44.0
	go.opentelemetry.io/otel/sdk v1.44.0
	go.opentelemetry.io/otel/trace v1.44.0
	go.uber.org/mock v0.6.0
	golang.org/x/time v0.15.0
	google.golang.org/grpc v1.82.1
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.6.2
	gopkg.in/yaml.v3 v3.0.1
	lukechampine.com/blake3 v1.4.1
)

require (
	filippo.io/bigmod v0.1.1-0.20260103110540-f8a47775ebe5 // indirect
	filippo.io/keygen v1.0.0 // indirect
	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20260607022201-88e0521b82d3 // indirect
	github.com/alecthomas/atomic v0.1.0-alpha2 // indirect
	github.com/anacrolix/btree v0.1.1 // indirect
	github.com/anacrolix/chansync v0.8.0 // indirect
	github.com/anacrolix/dht/v2 v2.24.0 // indirect
	github.com/anacrolix/envpprof v1.5.0 // indirect
	github.com/anacrolix/generics v0.2.0 // indirect
	github.com/anacrolix/go-libutp v1.4.0 // indirect
	github.com/anacrolix/log v0.17.1-0.20251118025802-918f1157b7bb // indirect
	github.com/anacrolix/missinggo v1.3.0 // indirect
	github.com/anacrolix/missinggo/perf v1.0.0 // indirect
	github.com/anacrolix/missinggo/v2 v2.10.0 // indirect
	github.com/anacrolix/mmsg v1.1.1 // indirect
	github.com/anacrolix/multiless v0.4.0 // indirect
	github.com/anacrolix/stm v0.5.0 // indirect
	github.com/anacrolix/sync v0.6.0 // indirect
	github.com/anacrolix/upnp v0.1.4 // indirect
	github.com/anacrolix/utp v0.2.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/benbjohnson/clock v1.3.5 // indirect
	github.com/benbjohnson/immutable v0.4.3 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bits-and-blooms/bitset v1.24.5 // indirect
	github.com/bradfitz/iter v0.0.0-20191230175014-e8f45d346db8 // indirect
	github.com/cespare/xxhash v1.1.0 // indirect
	github.com/cilium/ebpf v0.21.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.7 // indirect
	github.com/davidlazar/go-crypto v0.0.0-20200604182044-b73af7476f6c // indirect
	github.com/dlclark/regexp2/v2 v2.2.2 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/dunglas/httpsfv v1.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/emicklei/dot v1.6.2 // indirect
	github.com/ethereum/c-kzg-4844/v2 v2.1.8 // indirect
	github.com/felixge/fgprof v0.9.5 // indirect
	github.com/ferranbt/fastssz v0.1.4 // indirect
	github.com/filecoin-project/go-clock v0.1.0 // indirect
	github.com/flynn/noise v1.1.0 // indirect
	github.com/garslo/gogen v0.0.0-20230926014519-f497ca02dd4c // indirect
	github.com/go-kit/log v0.2.1 // indirect
	github.com/go-llsqlite/adapter v0.2.0 // indirect
	github.com/go-llsqlite/crawshaw v0.6.0 // indirect
	github.com/go-logfmt/logfmt v0.6.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/gopacket v1.1.19 // indirect
	github.com/google/pprof v0.0.0-20260709232956-b9395ee17fa0 // indirect
	github.com/huandu/xstrings v1.5.0 // indirect
	github.com/huin/goupnp v1.3.0 // indirect
	github.com/ianlancetaylor/cgosymbolizer v0.0.0-20260504013507-f4b012c11129 // indirect
	github.com/ipfs/boxo v0.41.0 // indirect
	github.com/ipfs/go-cid v0.6.2 // indirect
	github.com/ipfs/go-datastore v0.9.2 // indirect
	github.com/ipfs/go-log/v2 v2.9.2 // indirect
	github.com/ipld/go-ipld-prime v0.24.0 // indirect
	github.com/jackpal/go-nat-pmp v1.0.2 // indirect
	github.com/jbenet/go-temp-err-catcher v0.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/koron/go-ssdp v0.9.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/libp2p/go-buffer-pool v0.1.0 // indirect
	github.com/libp2p/go-cidranger v1.1.0 // indirect
	github.com/libp2p/go-flow-metrics v0.3.0 // indirect
	github.com/libp2p/go-libp2p-asn-util v0.4.1 // indirect
	github.com/libp2p/go-libp2p-kbucket v0.8.0 // indirect
	github.com/libp2p/go-libp2p-record v0.3.1 // indirect
	github.com/libp2p/go-libp2p-routing-helpers v0.7.5 // indirect
	github.com/libp2p/go-msgio v0.3.0 // indirect
	github.com/libp2p/go-netroute v0.4.0 // indirect
	github.com/libp2p/go-reuseport v0.4.0 // indirect
	github.com/libp2p/go-yamux/v5 v5.1.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20260330125221-c963978e514e // indirect
	github.com/marten-seemann/tcp v0.0.0-20210406111302-dfbc87cc63fd // indirect
	github.com/mattn/go-runewidth v0.0.24 // indirect
	github.com/mikioh/tcpinfo v0.0.0-20190314235526-30a79bb1804b // indirect
	github.com/mikioh/tcpopt v0.0.0-20190314235656-172688c1accc // indirect
	github.com/minio/sha256-simd v1.0.1 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/mitchellh/pointerstructure v1.2.1 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/mr-tron/base58 v1.3.0 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/multiformats/go-base32 v0.1.0 // indirect
	github.com/multiformats/go-base36 v0.2.0 // indirect
	github.com/multiformats/go-multiaddr-dns v0.5.0 // indirect
	github.com/multiformats/go-multiaddr-fmt v0.1.0 // indirect
	github.com/multiformats/go-multibase v0.3.0 // indirect
	github.com/multiformats/go-multicodec v0.10.0 // indirect
	github.com/multiformats/go-multihash v0.2.3 // indirect
	github.com/multiformats/go-multistream v0.6.1 // indirect
	github.com/multiformats/go-varint v0.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/nxadm/tail v1.4.11 // indirect
	github.com/opencontainers/runtime-spec v1.3.0 // indirect
	github.com/pion/datachannel v1.6.2 // indirect
	github.com/pion/dtls/v3 v3.1.5 // indirect
	github.com/pion/ice/v4 v4.2.7 // indirect
	github.com/pion/interceptor v0.1.45 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.17 // indirect
	github.com/pion/rtp v1.10.3 // indirect
	github.com/pion/sctp v1.10.3 // indirect
	github.com/pion/sdp/v3 v3.0.19 // indirect
	github.com/pion/srtp/v3 v3.0.12 // indirect
	github.com/pion/stun/v3 v3.1.6 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/pion/turn/v5 v5.0.12 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/polydawn/refmt v0.90.0 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/protolambda/ctxlock v0.1.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.60.0 // indirect
	github.com/quic-go/webtransport-go v0.11.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rogpeppe/go-internal v1.15.0 // indirect
	github.com/rs/dnscache v0.0.0-20230804202142-fc85eb664529 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/shirou/gopsutil v3.21.11+incompatible // indirect
	github.com/syndtr/goleveldb v1.0.1-0.20210819022825-2ae1ddf74ef7 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/valyala/fastrand v1.1.0 // indirect
	github.com/valyala/histogram v1.2.0 // indirect
	github.com/whyrusleeping/go-keyspace v0.0.0-20160322163242-5b898ac5add1 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/xrash/smetrics v0.0.0-20250705151800-55b8f293f342 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.etcd.io/bbolt v1.5.0 // indirect
	go.mongodb.org/mongo-driver v1.17.9 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.uber.org/dig v1.19.0 // indirect
	go.uber.org/fx v1.24.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/telemetry v0.0.0-20260708182218-49f421fb7959 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	gonum.org/v1/gonum v0.17.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
	gopkg.in/tomb.v1 v1.0.0-20141024135613-dd632973f1e7 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.53.0 // indirect
	zombiezen.com/go/sqlite v1.4.2 // indirect
)

//replace github.com/lucas-clemente/quic-go v0.29.0 => github.com/lucas-clemente/quic-go v0.28.1

//replace github.com/torquem-ch/mdbx-go v0.30.0 => github.com/N42/mdbx-go v0.0.0-20230203081605-fc0b6278d4f7

replace github.com/VictoriaMetrics/metrics => github.com/ledgerwatch/victoria-metrics v0.0.4

// S4 SECURITY NOTE: This fork is required for iOS build compatibility.
// The official elastic/gosigar PR #134 was declined. Consider:
// 1. Migrating to organization fork (github.com/n42blockchain/gosigar)
// 2. Using build tags to exclude on mobile platforms
// Risk: Third-party maintainer could introduce malicious code.
replace github.com/elastic/gosigar => github.com/Jackmeng1985/gosigar v0.14.2-fix-ios

replace github.com/erigontech/erigon-snapshot => github.com/ledgerwatch/erigon-snapshot v1.3.1-0.20240805114253-42da880260bb

replace github.com/erigontech/interfaces => github.com/ledgerwatch/interfaces v0.0.0-20241024161200-024ffe1cabff
