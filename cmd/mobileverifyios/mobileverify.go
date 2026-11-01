// Copyright 2022-2026 The N42 Authors
// SPDX-License-Identifier: LGPL-3.0-or-later

// Package mobileverify exposes the string-only V3 API to Objective-C/Swift.
// Binding cmd/evmsdk directly also exports legacy packet/read-log types that
// gobind cannot represent in Objective-C. This facade reuses the same verifier.
package mobileverify

import (
	"errors"

	"github.com/n42blockchain/N42/cmd/evmsdk"
)

// GenerateKey returns a new BLS secret seed. Persist it only in secure storage.
func GenerateKey() (string, error) {
	key, err := evmsdk.GenerateMobileBLSKey()
	if err != nil {
		return "", err
	}
	return key.PrivHex, nil
}

// PublicKey derives the device's stable registry identity.
func PublicKey(privateKey string) (string, error) {
	return evmsdk.MobileBLSPublicKey(privateKey)
}

// Miner owns the native continuous loop. No key or internal type is exported.
type Miner struct{ inner *evmsdk.MobileVerifyMiner }

// NewMiner creates a stopped miner for the selected node and chain.
func NewMiner(baseURL, privateKey, chain string) (*Miner, error) {
	inner, err := evmsdk.NewMobileVerifyMiner(baseURL, privateKey, chain)
	if err != nil {
		return nil, err
	}
	return &Miner{inner: inner}, nil
}

func (m *Miner) PubkeyHex() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.PubkeyHex()
}

func (m *Miner) Start() {
	if m != nil && m.inner != nil {
		m.inner.Start()
	}
}

func (m *Miner) Stop() {
	if m != nil && m.inner != nil {
		m.inner.Stop()
	}
}

func (m *Miner) Running() bool { return m != nil && m.inner != nil && m.inner.Running() }

func (m *Miner) Status() string {
	if m == nil || m.inner == nil {
		return `{"running":false,"phase":"idle"}`
	}
	return m.inner.Status()
}

// Session exposes the probe calls without leaking any packet structs.
type Session struct{ inner *evmsdk.MobileVerifySession }

func NewSession(baseURL, privateKey, chain string) (*Session, error) {
	inner, err := evmsdk.NewMobileVerifySession(baseURL, privateKey, chain)
	if err != nil {
		return nil, err
	}
	return &Session{inner: inner}, nil
}

func (s *Session) PubkeyHex() string {
	if s == nil || s.inner == nil {
		return ""
	}
	return s.inner.PubkeyHex()
}

func (s *Session) Close() {
	if s != nil && s.inner != nil {
		s.inner.Close()
	}
}

func (s *Session) current() (*evmsdk.MobileVerifySession, error) {
	if s == nil || s.inner == nil {
		return nil, errors.New("mobileverify: uninitialized session")
	}
	return s.inner, nil
}

func (s *Session) Register() (string, error) {
	inner, err := s.current()
	if err != nil {
		return "", err
	}
	return inner.Register()
}

func (s *Session) Latest() (string, error) {
	inner, err := s.current()
	if err != nil {
		return "", err
	}
	return inner.Latest()
}

func (s *Session) VerifyBlock(hash string) (string, error) {
	inner, err := s.current()
	if err != nil {
		return "", err
	}
	return inner.VerifyBlock(hash)
}

func (s *Session) Cert(hash string) (string, error) {
	inner, err := s.current()
	if err != nil {
		return "", err
	}
	return inner.Cert(hash)
}

func (s *Session) Health() (string, error) {
	inner, err := s.current()
	if err != nil {
		return "", err
	}
	return inner.Health()
}
