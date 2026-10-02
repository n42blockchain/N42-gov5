package engineapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func newTestJWTServer(secret []byte) *httptest.Server {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(newJWTHandler(secret, inner))
}

func signToken(t *testing.T, secret []byte, claims jwt.RegisteredClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestJWTHandler_MissingToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	srv := newTestJWTServer(secret)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestJWTHandler_BadSignature(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	wrongSecret := []byte("ffffffffffffffffffffffffffffffff")
	srv := newTestJWTServer(secret)
	defer srv.Close()

	tok := signToken(t, wrongSecret, jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestJWTHandler_MissingIssuedAt(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	srv := newTestJWTServer(secret)
	defer srv.Close()

	tok := signToken(t, secret, jwt.RegisteredClaims{}) // no IssuedAt
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestJWTHandler_StaleToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	srv := newTestJWTServer(secret)
	defer srv.Close()

	old := time.Now().Add(-5 * time.Minute)
	tok := signToken(t, secret, jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(old)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestJWTHandler_FutureToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	srv := newTestJWTServer(secret)
	defer srv.Close()

	future := time.Now().Add(5 * time.Minute)
	tok := signToken(t, secret, jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(future)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestJWTHandler_ExpiredToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	srv := newTestJWTServer(secret)
	defer srv.Close()

	now := time.Now()
	tok := signToken(t, secret, jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(-time.Minute)),
	})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestJWTHandler_Valid(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	srv := newTestJWTServer(secret)
	defer srv.Close()

	tok := signToken(t, secret, jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
