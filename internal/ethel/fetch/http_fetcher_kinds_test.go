package fetch

import "testing"

// TestHTTPFetcher_Kinds covers both branches: HTTPS-only by default, and
// HTTPS+HTTP when AllowPlaintext is set.
func TestHTTPFetcher_Kinds(t *testing.T) {
	f := NewHTTPFetcher(HTTPFetcherOptions{})
	ks := f.Kinds()
	if len(ks) != 1 || ks[0] != SourceHTTPS {
		t.Errorf("default Kinds() = %v, want [SourceHTTPS]", ks)
	}

	fp := NewHTTPFetcher(HTTPFetcherOptions{AllowPlaintext: true})
	ks2 := fp.Kinds()
	if len(ks2) != 2 || ks2[0] != SourceHTTPS || ks2[1] != SourceHTTP {
		t.Errorf("plaintext Kinds() = %v, want [SourceHTTPS, SourceHTTP]", ks2)
	}
}
