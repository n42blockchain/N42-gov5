package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	metrics "github.com/n42blockchain/N42/common/metrics"
	chain "github.com/n42blockchain/N42/common/types"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// Sidecar calls POST /decide at a configured endpoint; it never fetches
// request-supplied input locations. Redirects are prohibited.
type Sidecar struct {
	endpoint string
	identity Identity
	token    string
	client   *http.Client
}

func NewSidecar(endpoint string, id Identity, timeout time.Duration) (*Sidecar, error) {
	return NewAuthenticatedSidecar(endpoint, id, timeout, "")
}
func NewAuthenticatedSidecar(endpoint string, id Identity, timeout time.Duration, token string) (*Sidecar, error) {
	if token != "" && (len(token) < 16 || len(token) > 256 || strings.ContainsAny(token, "\r\n\t ")) {
		return nil, errors.New("invalid sidecar token")
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid sidecar HTTP endpoint")
	}
	if id.DID == "" || id.ModelHash == (chain.Hash{}) || timeout <= 0 {
		return nil, errors.New("sidecar identity, pinned model hash and timeout required")
	}
	return &Sidecar{token: token, endpoint: endpoint, identity: id, client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("sidecar redirects prohibited") }}}, nil
}
func (s *Sidecar) Identity() Identity {
	id := s.identity
	id.Tasks = append([]string{}, id.Tasks...)
	id.Schemas = append([]string{}, id.Schemas...)
	return id
}
func (s *Sidecar) Decide(ctx context.Context, r d.DecisionRequest, input string) (d.DecisionResult, error) {
	started := time.Now()
	defer func() {
		metrics.GetOrCreateHistogram("ddn_sidecar_latency_seconds{provider_did=" + strconv.Quote(s.identity.DID) + "}").Observe(time.Since(started).Seconds())
	}()
	b, err := json.Marshal(struct {
		Request d.DecisionRequest `json:"request"`
		Input   string            `json:"input"`
	}{r, input})
	if err != nil {
		return d.DecisionResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(b))
	if err != nil {
		return d.DecisionResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return d.DecisionResult{}, errors.New("sidecar unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return d.DecisionResult{}, fmt.Errorf("sidecar HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return d.DecisionResult{}, errors.New("invalid sidecar response size")
	}
	var result struct {
		RequestID chain.Hash       `json:"request_id"`
		ModelHash chain.Hash       `json:"model_hash"`
		Result    d.DecisionResult `json:"result"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&result); err != nil {
		return d.DecisionResult{}, errors.New("invalid sidecar response")
	}
	if dec.Decode(new(any)) != io.EOF {
		return d.DecisionResult{}, errors.New("trailing sidecar response")
	}
	if result.RequestID != r.RequestID || result.ModelHash != s.identity.ModelHash {
		return d.DecisionResult{}, errors.New("sidecar request/model binding mismatch")
	}
	return result.Result, result.Result.ValidateSchema(r.SchemaID)
}
