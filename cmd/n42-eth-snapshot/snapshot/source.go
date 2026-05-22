package snapshot

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Source abstracts a snapshot mirror — local directory or remote
// HTTP server.
type Source interface {
	// Open returns a readable handle for the file at the given
	// relative path. Caller must Close.
	Open(relPath string) (io.ReadCloser, int64, error)
}

// OpenSource resolves a user-supplied source string to a Source.
// Accepted forms:
//
//	file:///abs/path    → local dir
//	/abs/path           → local dir (file:// prefix optional)
//	./relative/path     → local dir
//	http(s)://host/...  → HTTP mirror
func OpenSource(s string) (Source, error) {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("parse url %s: %w", s, err)
		}
		return &httpSource{base: u}, nil
	}
	dir := strings.TrimPrefix(s, "file://")
	dir = filepath.FromSlash(dir)
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("open source %s: %w", dir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("source %s is not a directory", dir)
	}
	return &localSource{root: dir}, nil
}

// localSource serves files from a local directory.
type localSource struct{ root string }

func (l *localSource) Open(relPath string) (io.ReadCloser, int64, error) {
	full := filepath.Join(l.root, filepath.FromSlash(relPath))
	f, err := os.Open(full)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// httpSource serves files from an HTTP server. Joins base URL with
// the relative path; expects the server to honour Content-Length.
type httpSource struct{ base *url.URL }

func (h *httpSource) Open(relPath string) (io.ReadCloser, int64, error) {
	// Manually join to avoid path.Clean swallowing trailing slashes.
	u := *h.base
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.Path += relPath
	resp, err := http.Get(u.String())
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("GET %s: %s", u.String(), resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}
