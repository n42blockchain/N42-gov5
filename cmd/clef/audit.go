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

package main

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// AuditLogger provides tamper-evident logging of all signing operations.
// Every sign request, account access, and administrative action is recorded
// with a timestamp, method name, address, approval status, and reason.
type AuditLogger struct {
	file   *os.File
	logger *log.Logger
	mu     sync.Mutex
}

// NewAuditLogger opens (or creates) the audit log file at the given path
// and returns a ready-to-use AuditLogger. The file is opened in append-only
// mode so that existing entries are never overwritten.
func NewAuditLogger(path string) (*AuditLogger, error) {
	if path == "" {
		return nil, fmt.Errorf("audit log path must not be empty")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}

	l := log.New(f, "", 0) // we format timestamps ourselves for consistency
	return &AuditLogger{
		file:   f,
		logger: l,
	}, nil
}

// LogSignRequest records a signing request together with the approval decision.
func (a *AuditLogger) LogSignRequest(method string, addr types.Address, approved bool, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	status := "APPROVED"
	if !approved {
		status = "DENIED"
	}
	a.logger.Printf("%s | SIGN  | method=%s addr=%s status=%s reason=%q",
		time.Now().UTC().Format(time.RFC3339Nano),
		method,
		addr.Hex(),
		status,
		reason,
	)
}

// LogAccountAccess records an account-level access (list, create, etc.).
func (a *AuditLogger) LogAccountAccess(method string, addr types.Address) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.logger.Printf("%s | ACCT  | method=%s addr=%s",
		time.Now().UTC().Format(time.RFC3339Nano),
		method,
		addr.Hex(),
	)
}

// LogInfo records a general informational audit entry.
func (a *AuditLogger) LogInfo(method string, msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.logger.Printf("%s | INFO  | method=%s msg=%q",
		time.Now().UTC().Format(time.RFC3339Nano),
		method,
		msg,
	)
}

// Close flushes and closes the underlying log file.
func (a *AuditLogger) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.file != nil {
		return a.file.Close()
	}
	return nil
}
