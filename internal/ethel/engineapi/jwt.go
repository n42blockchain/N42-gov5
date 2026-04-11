// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// JWT auth middleware for the Engine API HTTP listener. Forked verbatim
// from internal/node/jwt_handler.go so the eth-el binary does not pull in
// the entire internal/node package just for one ~60-line helper. Keep the
// two files in sync if either is updated.

package engineapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// jwtExpiryTimeout is the maximum allowed clock drift on the iat claim
// (Engine API spec section 3.1).
const jwtExpiryTimeout = 60 * time.Second

type jwtHandler struct {
	keyFunc func(token *jwt.Token) (interface{}, error)
	next    http.Handler
}

// newJWTHandler wraps next in HS256 bearer-token authentication.
func newJWTHandler(secret []byte, next http.Handler) http.Handler {
	return &jwtHandler{
		keyFunc: func(token *jwt.Token) (interface{}, error) { return secret, nil },
		next:    next,
	}
}

func (h *jwtHandler) ServeHTTP(out http.ResponseWriter, r *http.Request) {
	var (
		strToken string
		claims   jwt.RegisteredClaims
	)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		strToken = strings.TrimPrefix(auth, "Bearer ")
	}
	if len(strToken) == 0 {
		http.Error(out, "missing token", http.StatusForbidden)
		return
	}
	token, err := jwt.ParseWithClaims(strToken, &claims, h.keyFunc,
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithoutClaimsValidation())

	switch {
	case err != nil:
		http.Error(out, err.Error(), http.StatusForbidden)
	case !token.Valid:
		http.Error(out, "invalid token", http.StatusForbidden)
	case !claims.VerifyExpiresAt(time.Now(), false):
		http.Error(out, "token is expired", http.StatusForbidden)
	case claims.IssuedAt == nil:
		http.Error(out, "missing issued-at", http.StatusForbidden)
	case time.Since(claims.IssuedAt.Time) > jwtExpiryTimeout:
		http.Error(out, "stale token", http.StatusForbidden)
	case time.Until(claims.IssuedAt.Time) > jwtExpiryTimeout:
		http.Error(out, "future token", http.StatusForbidden)
	default:
		h.next.ServeHTTP(out, r)
	}
}
