package fetch

import "testing"

// TestNewTorrentFetcher_Defaults pins the production constructor's
// option-defaulting (mirrors newTorrentFetcher, already covered via fakes
// elsewhere, but NewTorrentFetcher itself — the real-client adapter path —
// was uncovered). A nil *storagetorrent.Client is fine here: construction
// never touches the client.
func TestNewTorrentFetcher_Defaults(t *testing.T) {
	f := NewTorrentFetcher(nil, TorrentFetcherOptions{})
	if f.opts.InfoTimeout == 0 || f.opts.ProgressInterval == 0 || f.opts.MaxAttempts == 0 {
		t.Errorf("expected defaults to be filled in, got %+v", f.opts)
	}
	if f.client == nil {
		t.Error("expected a non-nil client adapter")
	}
}
