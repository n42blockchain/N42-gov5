package p2p

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	"github.com/n42blockchain/N42/common/utils"
)

const (
	keyPath     = "network-keys"
	seqPath     = "network-seq"
	dialTimeout = 1 * time.Second
)

// SerializeENR takes the enr record in its key-value form and serializes it.
func SerializeENR(record *enr.Record) (string, error) {
	if record == nil {
		return "", errors.New("could not serialize nil record")
	}
	buf := new(bytes.Buffer)
	if err := record.EncodeRLP(buf); err != nil {
		return "", errors.Wrap(err, "could not encode ENR record to bytes")
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

func getSeqNumber(cfg *conf.P2PConfig) (*sync_pb.Ping, error) {
	seqFilePath := filepath.Join(cfg.DataDir, seqPath)

	src, err := os.ReadFile(seqFilePath) // #nosec G304
	if os.IsNotExist(err) {
		return &sync_pb.Ping{SeqNumber: 0}, nil
	}
	if err != nil {
		log.Error("Error reading network seqNumber from file", "err", err)
		return nil, err
	}
	if len(src) < 8 {
		log.Warn("Invalid seq number file size", "size", len(src), "expected", 8)
		return &sync_pb.Ping{SeqNumber: 0}, nil
	}

	seqNumber := binary.LittleEndian.Uint64(src)
	if seqNumber > 0 {
		log.Info("Load seq number from file", "seqNumber", seqNumber)
	}
	return &sync_pb.Ping{SeqNumber: seqNumber}, nil
}

func saveSeqNumber(cfg *conf.P2PConfig, seqNumber *sync_pb.Ping) error {
	seqFilePath := filepath.Join(cfg.DataDir, seqPath)
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, seqNumber.SeqNumber)
	if err := os.WriteFile(seqFilePath, b, 0600); err != nil {
		log.Error("Failed to save p2p seq number", "err", err)
		return err
	}
	log.Info("Wrote seq number to file", "seqNumber", seqNumber.SeqNumber)
	return nil
}

// privKey determines a private key for p2p networking from the service's
// configuration. Priority: CLI flag > existing file > generated key.
func privKey(cfg *conf.P2PConfig) (*ecdsa.PrivateKey, error) {
	defaultKeyPath := filepath.Join(cfg.DataDir, keyPath)

	// CLI-specified private key takes highest precedence.
	if cfg.PrivateKey != "" {
		return privKeyFromFile(cfg.PrivateKey)
	}

	if _, err := os.Stat(defaultKeyPath); err == nil {
		return privKeyFromFile(defaultKeyPath)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// No keys on the filesystem; generate a new one.
	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	// When StaticPeerID is set, persist the key so subsequent starts reuse it.
	if cfg.StaticPeerID {
		rawbytes, err := priv.Raw()
		if err != nil {
			return nil, err
		}
		dst := make([]byte, hex.EncodedLen(len(rawbytes)))
		hex.Encode(dst, rawbytes)
		if err := os.WriteFile(defaultKeyPath, dst, 0600); err != nil {
			return nil, err
		}
		log.Info("Generated new network key")
		// Re-read from file to guarantee consistency with future starts.
		return privKeyFromFile(defaultKeyPath)
	}
	return utils.ConvertFromInterfacePrivKey(priv)
}

// privKeyFromFile reads a hex-encoded secp256k1 private key from the given path.
func privKeyFromFile(path string) (*ecdsa.PrivateKey, error) {
	src, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		log.Error("Error reading private key from file", "err", err)
		return nil, err
	}
	// Key files are commonly created by shell/PowerShell text writers, which
	// append a trailing newline. Match the BLS keystore loader and accept
	// surrounding ASCII whitespace instead of rejecting an otherwise valid key.
	src = bytes.TrimSpace(src)
	dst := make([]byte, hex.DecodedLen(len(src)))
	if _, err = hex.Decode(dst, src); err != nil {
		return nil, errors.Wrap(err, "failed to decode hex string")
	}
	unmarshalledKey, err := crypto.UnmarshalSecp256k1PrivateKey(dst)
	if err != nil {
		return nil, err
	}
	return utils.ConvertFromInterfacePrivKey(unmarshalledKey)
}

// verifyConnectivity attempts to dial an address to confirm it is reachable.
func verifyConnectivity(addr string, port int, protocol string) {
	if addr == "" {
		return
	}
	hostPort := net.JoinHostPort(addr, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout(protocol, hostPort, dialTimeout)
	if err != nil {
		log.Warn("IP address is not accessible", "err", err, "protocol", protocol, "address", hostPort)
		return
	}
	if err := conn.Close(); err != nil {
		log.Debug("Could not close connection", "err", err)
	}
}
