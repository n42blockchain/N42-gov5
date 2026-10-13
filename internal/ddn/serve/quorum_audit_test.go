package serve

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

type auditRejectingProvider struct{ provider.DecisionProvider }

func (auditRejectingProvider) ValidateRequest(d.DecisionRequest) error {
	return errors.New("configured provider group rejected request")
}

func TestAuditProviderCannotBypassQuorumOrModelRequirements(t *testing.T) {
	p, err := provider.NewNativeRules("did:n42:audit")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"quorum", "model", "family", "validator"} {
		t.Run(mode, func(t *testing.T) {
			var backend provider.DecisionProvider = p
			r := request()
			r.Quorum = 2
			switch mode {
			case "model":
				r.ModelRequirements.ModelHash = chain.Hash{1}
			case "family":
				r.ModelRequirements.Family = "unavailable-family"
			case "validator":
				r.Quorum = 1
				backend = auditRejectingProvider{p}
			}
			r.RequestID, _ = r.CanonicalHash()
			s, err := New(config(), backend)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/decide", bytes.NewReader(payload(r, "disk full")))
			req.Header.Set("Authorization", "Bearer "+token)
			s.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s restriction bypassed: status %d", mode, w.Code)
			}
		})
	}
}
