package node

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/require"
)

func signTestToken(t *testing.T, secret []byte, issuedAt time.Time) string {
	t.Helper()
	claims := jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issuedAt)}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(secret)
	require.NoError(t, err)
	return s
}

func newJWTTestHandler() (http.Handler, []byte, *bool) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	return newJWTHandler(secret, next), secret, &called
}

func TestJWTHandlerMissingToken(t *testing.T) {
	h, _, called := newJWTTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, *called)
}

func TestJWTHandlerValidToken(t *testing.T) {
	h, secret, called := newJWTTestHandler()
	tok := signTestToken(t, secret, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, *called)
}

func TestJWTHandlerInvalidSignature(t *testing.T) {
	h, _, called := newJWTTestHandler()
	tok := signTestToken(t, []byte("wrong-secret-wrong-secret-wrong"), time.Now())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, *called)
}

func TestJWTHandlerMissingIssuedAt(t *testing.T) {
	h, secret, called := newJWTTestHandler()
	claims := jwt.RegisteredClaims{} // no IssuedAt
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(secret)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+s)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, *called)
	require.Contains(t, rec.Body.String(), "missing issued-at")
}

func TestJWTHandlerStaleToken(t *testing.T) {
	h, secret, called := newJWTTestHandler()
	tok := signTestToken(t, secret, time.Now().Add(-2*jwtExpiryTimeout))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, *called)
	require.Contains(t, rec.Body.String(), "stale token")
}

func TestJWTHandlerFutureToken(t *testing.T) {
	h, secret, called := newJWTTestHandler()
	tok := signTestToken(t, secret, time.Now().Add(2*jwtExpiryTimeout))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, *called)
	require.Contains(t, rec.Body.String(), "future token")
}

func TestJWTHandlerWrongSigningMethod(t *testing.T) {
	h, _, called := newJWTTestHandler()
	// alg "none" tokens are rejected by WithValidMethods([]string{"HS256"}).
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())})
	s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+s)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, *called)
}
