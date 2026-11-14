// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Key-store directory, account-manager backends, metrics setup and etherbase.

package node

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/n42blockchain/N42/accounts/external"
	"github.com/n42blockchain/N42/accounts/keystore"
	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	nodeMetrics "github.com/n42blockchain/N42/internal/metrics"
	libmetrics "github.com/n42blockchain/N42/lib/metrics"
	"github.com/n42blockchain/N42/log"
)

// getKeyStoreDir retrieves the key directory and will create
// and ephemeral one if necessary.
func getKeyStoreDir(conf *conf.NodeConfig) (string, bool, error) {
	keydir, err := conf.KeyDirConfig()
	if err != nil {
		return "", false, err
	}
	isEphemeral := false
	if keydir == "" {
		// There is no datadir.
		keydir, err = os.MkdirTemp("", "go-ethereum-keystore")
		isEphemeral = true
	}

	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(keydir, 0700); err != nil {
		return "", false, err
	}

	return keydir, isEphemeral, nil
}

func setAccountManagerBackends(stack *Node, conf *conf.NodeConfig) error {
	am := stack.AccountManager()
	keydir := stack.KeyStoreDir()
	scryptN := keystore.StandardScryptN
	scryptP := keystore.StandardScryptP
	if conf.UseLightweightKDF {
		scryptN = keystore.LightScryptN
		scryptP = keystore.LightScryptP
	}

	// Use EITHER external signer OR local signers to avoid duplicate accounts
	// and race conditions between the two backends.
	if conf.ExternalSigner != "" {
		log.Info("Using external signer", "endpoint", conf.ExternalSigner)
		extBackend, err := external.NewExternalBackend(conf.ExternalSigner)
		if err != nil {
			return fmt.Errorf("external signer at %s: %w", conf.ExternalSigner, err)
		}
		am.AddBackend(extBackend)
	} else {
		am.AddBackend(keystore.NewKeyStore(keydir, scryptN, scryptP))
	}

	return nil
}

// KeyStoreDir retrieves the key directory
func (n *Node) KeyStoreDir() string {
	return n.keyDir
}

func (n *Node) SetupMetrics(config conf.MetricsConfig) {
	if !config.Enable {
		return
	}

	// Register Go runtime and system-level metrics.
	nodeMetrics.RegisterSystemMetrics()

	// Attach lib/metrics' default Set to the registry this endpoint serves.
	// Without this the whole storage-layer family -- db_pgops{phase=...},
	// kvcache, txpool, layered, disk, mem -- is updated on every MDBX commit
	// (MdbxTx.Commit calls CollectMetrics unconditionally) and readable from
	// nowhere: lib/metrics.Setup is the only thing that registers that Set and
	// it has no callers. Registering here keeps one endpoint and one port
	// rather than starting the second HTTP server Setup would.
	prometheus.RegisterCollector(libmetrics.DefaultSet())

	if config.HTTP != "" {
		address := net.JoinHostPort(config.HTTP, strconv.Itoa(config.Port))
		log.Info("Enabling stand-alone metrics HTTP endpoint", "address", address)
		prometheus.Setup(address, log.Root())
	} else if config.Port != 0 {
		log.Warn("--metrics.port specified without --metrics.addr, metrics server will not start.")
	}
}

func (s *Node) Etherbase() (eb types.Address, err error) {
	s.lock.RLock()
	etherbase := s.etherbase
	s.lock.RUnlock()

	if etherbase != (types.Address{}) {
		return etherbase, nil
	}
	if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			etherbase := accounts[0].Address

			s.lock.Lock()
			s.etherbase = etherbase
			s.lock.Unlock()

			log.Info("Etherbase automatically configured", "address", etherbase)
			return etherbase, nil
		}
	}
	return types.Address{}, errors.New("etherbase must be explicitly specified")
}
