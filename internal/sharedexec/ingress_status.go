package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type ingressStatusRequest struct {
	Identity Identity `json:"identity"`
}

func (s *Server) serveIngressStatus(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request ingressStatusRequest
	if err := decoder.Decode(&request); err != nil || request.Identity != s.identity {
		http.Error(w, "invalid ingress status identity/request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one ingress status request required", http.StatusBadRequest)
		return
	}
	if s.ctx.Err() != nil {
		http.Error(w, "execution owner closed", http.StatusServiceUnavailable)
		return
	}
	status, err := s.StrictIngressStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

// StrictIngressStatus explicitly discovers one current volatile session. A
// producer pins it once at startup; this method never retries or refreshes it.
func (c *Client) StrictIngressStatus(ctx context.Context) (StrictIngressStatus, error) {
	raw, err := json.Marshal(ingressStatusRequest{Identity: c.identity})
	if err != nil {
		return StrictIngressStatus{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/ingress-status", bytes.NewReader(raw))
	if err != nil {
		return StrictIngressStatus{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return StrictIngressStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return StrictIngressStatus{}, errors.New("owner strict ingress unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(data) > 8192 {
		return StrictIngressStatus{}, errors.New("invalid ingress status response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var status StrictIngressStatus
	if err := decoder.Decode(&status); err != nil {
		return StrictIngressStatus{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return StrictIngressStatus{}, errors.New("trailing ingress status response")
	}
	if status.Identity != c.identity || status.Epoch == ([16]byte{}) || status.ManifestHash == ([32]byte{}) || status.CanonicalHash == ([32]byte{}) || (status.CanonicalNumber == 0 && status.CanonicalHash != c.identity.GenesisHash) || status.PendingTransactions < 0 || status.ReservedTransactions < 0 || status.PendingTransactions > 2_000_000 || status.ReservedTransactions > 2_000_000-status.PendingTransactions || status.RetainedEncodedBytes < 0 || status.ReservedEncodedBytes < 0 || status.RetainedEncodedBytes > 512<<20 || status.ReservedEncodedBytes > (512<<20)-status.RetainedEncodedBytes {
		return StrictIngressStatus{}, errors.New("ingress status binding/counter mismatch")
	}
	return status, nil
}
