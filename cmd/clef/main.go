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

// Package main implements the Clef external signer for N42.
// Clef is a standalone signing daemon that manages keys externally
// from the main node process. It exposes an IPC-based JSON-RPC API
// for signing transactions and data, with rule-based approval and
// comprehensive audit logging.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

var (
	keystoreFlag = &cli.StringFlag{
		Name:  "keystore",
		Usage: "Keystore directory path",
		Value: filepath.Join(defaultConfigDir(), "keystore"),
	}
	configdirFlag = &cli.StringFlag{
		Name:  "configdir",
		Usage: "Configuration directory path",
		Value: defaultConfigDir(),
	}
	chainIDFlag = &cli.Uint64Flag{
		Name:  "chainid",
		Usage: "Chain ID for transaction signing",
		Value: 42,
	}
	lightkdfFlag = &cli.BoolFlag{
		Name:  "lightkdf",
		Usage: "Reduce key-derivation RAM and CPU usage at some expense of KDF strength",
	}
	ipcpathFlag = &cli.StringFlag{
		Name:  "ipcpath",
		Usage: "IPC socket/pipe path for the signing server",
		Value: "clef.ipc",
	}
	rulesFlag = &cli.StringFlag{
		Name:  "rules",
		Usage: "Path to the JSON rules configuration file",
	}
	auditlogFlag = &cli.StringFlag{
		Name:  "auditlog",
		Usage: "Path to the audit log file",
		Value: "audit.log",
	}
)

func defaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".clef"
	}
	return filepath.Join(home, ".clef")
}

func main() {
	app := &cli.App{
		Name:    "clef",
		Usage:   "N42 external signer (Clef)",
		Version: clefVersion,
		Flags: []cli.Flag{
			keystoreFlag,
			configdirFlag,
			chainIDFlag,
			lightkdfFlag,
			ipcpathFlag,
			rulesFlag,
			auditlogFlag,
		},
		Commands: []*cli.Command{
			initCommand,
			attestCommand,
			setpwCommand,
		},
		Action:    startSigningServer,
		Copyright: "Copyright 2022-2026 The N42 Authors",
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// initCommand creates the clef configuration directory and generates
// a master seed file used for deriving internal encryption keys.
var initCommand = &cli.Command{
	Name:  "init",
	Usage: "Initialize Clef — create master seed and config directory",
	Action: func(c *cli.Context) error {
		configDir := c.String("configdir")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("create config directory %s: %w", configDir, err)
		}

		seedPath := filepath.Join(configDir, "masterseed.dat")
		if _, err := os.Stat(seedPath); err == nil {
			return fmt.Errorf("master seed already exists at %s — remove it first to reinitialize", seedPath)
		}

		// Generate 32 bytes of cryptographic randomness.
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return fmt.Errorf("generate master seed: %w", err)
		}

		if err := os.WriteFile(seedPath, []byte(hex.EncodeToString(seed)), 0600); err != nil {
			return fmt.Errorf("write master seed: %w", err)
		}

		fmt.Printf("Clef initialized. Master seed written to %s\n", seedPath)
		fmt.Println("IMPORTANT: Back up this file securely. Loss of the master seed is unrecoverable.")
		return nil
	},
}

// attestCommand records a cryptographic attestation of a rules configuration
// file, binding the rule set to the clef instance.
var attestCommand = &cli.Command{
	Name:      "attest",
	Usage:     "Attest a rules configuration file (record its SHA-256 hash)",
	ArgsUsage: "<sha256sum>",
	Action: func(c *cli.Context) error {
		if c.NArg() < 1 {
			return fmt.Errorf("usage: clef attest <sha256sum-of-rules-file>")
		}
		configDir := c.String("configdir")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}

		hash := c.Args().First()
		if len(hash) != 64 {
			return fmt.Errorf("expected 64-character hex SHA-256 hash, got %d characters", len(hash))
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("invalid hex in attestation hash: %w", err)
		}

		attestPath := filepath.Join(configDir, "attestation.txt")
		if err := os.WriteFile(attestPath, []byte(hash+"\n"), 0600); err != nil {
			return fmt.Errorf("write attestation: %w", err)
		}
		fmt.Printf("Attestation recorded at %s\n", attestPath)
		return nil
	},
}

// setpwCommand sets the passphrase for a keystore account.
var setpwCommand = &cli.Command{
	Name:      "setpw",
	Usage:     "Set or update the password for a keystore account",
	ArgsUsage: "<address>",
	Action: func(c *cli.Context) error {
		if c.NArg() < 1 {
			return fmt.Errorf("usage: clef setpw <address>")
		}
		// This is a placeholder; in production, the password would be read
		// from a secure terminal prompt (e.g. golang.org/x/term).
		fmt.Println("Password management requires interactive terminal input.")
		fmt.Println("Use the keystore directly or integrate with a secrets manager.")
		return nil
	},
}

// startSigningServer is the default action: it starts the IPC-based
// signing server that serves the Clef JSON-RPC API.
func startSigningServer(c *cli.Context) error {
	// Determine KDF parameters.
	scryptN := keystore.StandardScryptN
	scryptP := keystore.StandardScryptP
	if c.Bool("lightkdf") {
		scryptN = keystore.LightScryptN
		scryptP = keystore.LightScryptP
	}

	// Ensure keystore directory exists.
	keystoreDir := c.String("keystore")
	if err := os.MkdirAll(keystoreDir, 0700); err != nil {
		return fmt.Errorf("create keystore directory %s: %w", keystoreDir, err)
	}

	// Open the keystore.
	ks := keystore.NewKeyStore(keystoreDir, scryptN, scryptP)

	// Chain ID.
	chainID := new(big.Int).SetUint64(c.Uint64("chainid"))

	// Load rules.
	rulesPath := c.String("rules")
	rules, err := NewRuleEngine(rulesPath)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	if rulesPath != "" {
		log.Info("Rules engine loaded", "path", rulesPath)
	}

	// Open audit log.
	var audit *AuditLogger
	auditPath := c.String("auditlog")
	if auditPath != "" {
		audit, err = NewAuditLogger(auditPath)
		if err != nil {
			return fmt.Errorf("open audit log: %w", err)
		}
		defer audit.Close()
		log.Info("Audit logging enabled", "path", auditPath)
	}

	// Create the signer service.
	signer := NewSignerService(ks, chainID, rules, audit)

	// Set up the JSON-RPC server.
	server := jsonrpc.NewServer()
	if err := server.RegisterName("account", signer); err != nil {
		return fmt.Errorf("register signer service: %w", err)
	}

	// Determine the IPC path.
	ipcPath := c.String("ipcpath")
	if !filepath.IsAbs(ipcPath) {
		configDir := c.String("configdir")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
		ipcPath = filepath.Join(configDir, ipcPath)
	}

	// Remove stale socket file if it exists.
	if _, err := os.Stat(ipcPath); err == nil {
		if err := os.Remove(ipcPath); err != nil {
			return fmt.Errorf("remove stale IPC socket %s: %w", ipcPath, err)
		}
	}

	// Start the IPC listener.
	listener, err := net.Listen("unix", ipcPath)
	if err != nil {
		return fmt.Errorf("listen on IPC %s: %w", ipcPath, err)
	}
	defer listener.Close()

	// Set restrictive permissions on the socket file.
	if err := os.Chmod(ipcPath, 0600); err != nil {
		log.Warn("Failed to set IPC socket permissions", "err", err)
	}

	log.Info("Clef signer started",
		"ipc", ipcPath,
		"keystore", keystoreDir,
		"chainid", chainID.String(),
	)

	if audit != nil {
		audit.LogInfo("StartServer", fmt.Sprintf("ipc=%s keystore=%s chainid=%s", ipcPath, keystoreDir, chainID.String()))
	}

	// Accept connections in a goroutine.
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				// Listener was closed (shutdown).
				return
			}
			go server.ServeCodec(jsonrpc.NewCodec(conn), 0)
		}
	}()

	// Wait for termination signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	log.Info("Clef signer shutting down", "signal", sig)
	if audit != nil {
		audit.LogInfo("StopServer", fmt.Sprintf("signal=%v", sig))
	}

	return nil
}
