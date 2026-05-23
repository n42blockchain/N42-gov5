package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatus_FullyCurrent: client's local manifest matches the
// publisher's latest — status reports up-to-date with delta=0.
func TestStatus_FullyCurrent(t *testing.T) {
	src := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, src, "minimal", 25000); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	mSrc, _ := ManifestFor(src, "minimal")

	// Publish into mirror layout (root/<network>/<height>/<mode>/).
	mirror := t.TempDir()
	publishFakeMirror(t, src, mirror, "simnet", "minimal", mSrc)

	// Client is a verbatim copy of src.
	client := t.TempDir()
	rep, err := Fetch("file://"+filepath.Join(mirror, "simnet", "25000", "minimal"),
		client, "minimal", false, false, 2)
	if err != nil || !rep.OK {
		t.Fatalf("fetch: %v %+v", err, rep)
	}

	st, err := Status(client, "file://"+filepath.Join(mirror, "simnet"), "minimal")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.LocalHeight != 25000 {
		t.Errorf("LocalHeight=%d want 25000", st.LocalHeight)
	}
	if st.RemoteHeight != 25000 {
		t.Errorf("RemoteHeight=%d want 25000", st.RemoteHeight)
	}
	if st.BehindBlocks != 0 {
		t.Errorf("BehindBlocks=%d want 0", st.BehindBlocks)
	}
	if !st.UpToDate {
		t.Errorf("UpToDate=false want true")
	}
}

// TestStatus_BehindByOneRelease: client at H, publisher latest at
// H+1000, status reports BehindBlocks=1000.
func TestStatus_BehindByOneRelease(t *testing.T) {
	src1 := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, src1, "minimal", 24000); err != nil {
		t.Fatalf("manifest 24000: %v", err)
	}
	m1, _ := ManifestFor(src1, "minimal")

	src2 := touchFakeArchive(t)
	// Tweak a file so manifest_id changes.
	_ = os.WriteFile(filepath.Join(src2, "chain/freezer/headerc.cidx"),
		[]byte("v2"), 0o644)
	if err := writeFakeManifestWithHeight(t, src2, "minimal", 25000); err != nil {
		t.Fatalf("manifest 25000: %v", err)
	}
	m2, _ := ManifestFor(src2, "minimal")

	mirror := t.TempDir()
	publishFakeMirror(t, src1, mirror, "simnet", "minimal", m1)
	publishFakeMirror(t, src2, mirror, "simnet", "minimal", m2)

	// Client starts at the older release.
	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "24000", "minimal"),
		client, "minimal", false, false, 2)

	st, err := Status(client, "file://"+filepath.Join(mirror, "simnet"), "minimal")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.LocalHeight != 24000 {
		t.Errorf("LocalHeight=%d want 24000", st.LocalHeight)
	}
	if st.RemoteHeight != 25000 {
		t.Errorf("RemoteHeight=%d want 25000", st.RemoteHeight)
	}
	if st.BehindBlocks != 1000 {
		t.Errorf("BehindBlocks=%d want 1000", st.BehindBlocks)
	}
	if st.UpToDate {
		t.Errorf("UpToDate=true want false")
	}
}

// TestStatus_NoLocalManifest: client datadir is empty; status
// reports LocalHeight=0 and BehindBlocks=RemoteHeight.
func TestStatus_NoLocalManifest(t *testing.T) {
	src := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, src, "minimal", 25000); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	m, _ := ManifestFor(src, "minimal")
	mirror := t.TempDir()
	publishFakeMirror(t, src, mirror, "simnet", "minimal", m)

	client := t.TempDir() // empty

	st, err := Status(client, "file://"+filepath.Join(mirror, "simnet"), "minimal")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.LocalHeight != 0 {
		t.Errorf("LocalHeight=%d want 0 (empty client)", st.LocalHeight)
	}
	if st.RemoteHeight != 25000 {
		t.Errorf("RemoteHeight=%d want 25000", st.RemoteHeight)
	}
	if st.BehindBlocks != 25000 {
		t.Errorf("BehindBlocks=%d want 25000", st.BehindBlocks)
	}
	if st.UpToDate {
		t.Errorf("UpToDate=true on empty client")
	}
	if !strings.Contains(st.Note, "no local manifest") {
		t.Errorf("Note=%q want 'no local manifest' hint", st.Note)
	}
}

// TestStatus_NoRemoteIndex: source has no releases.json (publisher
// has never published, or mirror URL is wrong). Should error
// clearly, not silently report 0.
func TestStatus_NoRemoteIndex(t *testing.T) {
	client := t.TempDir()
	src := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, client, "minimal", 12345); err != nil {
		t.Fatalf("local manifest: %v", err)
	}
	_ = src // unused but documents intent

	empty := t.TempDir() // no releases.json
	_, err := Status(client, "file://"+empty, "minimal")
	if err == nil {
		t.Errorf("Status with no releases.json should error")
	}
}

// --- helpers ---

// publishFakeMirror writes the file layout n42-eth-publish produces:
//   <mirror>/<network>/<height>/<mode>/<files...>
//   <mirror>/<network>/releases.json
func publishFakeMirror(t *testing.T, src, mirror, network, mode string, m interface{}) {
	t.Helper()
	man, ok := m.(*struct{}) // sentinel cast — not used; below uses real type
	_ = man
	_ = ok

	// Re-read the manifest to get the typed object (avoids import cycle issues).
	manRel := "manifest-" + mode + ".json"
	manData, err := os.ReadFile(filepath.Join(src, manRel))
	if err != nil {
		t.Fatalf("read src manifest: %v", err)
	}
	var raw struct {
		Network    string `json:"network"`
		Height     uint64 `json:"height"`
		Mode       string `json:"mode"`
		CreatedAt  string `json:"created_at"`
		ManifestID string `json:"manifest_id"`
		Files      []struct {
			Path       string `json:"path"`
			Section    string `json:"section"`
			Size       int64  `json:"size"`
			Blake2b256 string `json:"blake2b256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manData, &raw); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	dstDir := filepath.Join(mirror, network, fmtHeight(raw.Height), mode)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, f := range raw.Files {
		srcFull := filepath.Join(src, filepath.FromSlash(f.Path))
		dstFull := filepath.Join(dstDir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dstFull), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(dstFull), err)
		}
		if err := copyTestFile(srcFull, dstFull); err != nil {
			t.Fatalf("copy %s: %v", f.Path, err)
		}
	}
	if err := copyTestFile(filepath.Join(src, manRel), filepath.Join(dstDir, manRel)); err != nil {
		t.Fatalf("copy manifest: %v", err)
	}

	// Maintain releases.json at <mirror>/<network>/releases.json.
	idxPath := filepath.Join(mirror, network, "releases.json")
	var idx struct {
		Network  string                       `json:"network"`
		Latest   map[string]*publishedRel     `json:"latest"`
		Releases []*publishedRel              `json:"releases"`
		Updated  string                       `json:"updated_at"`
	}
	if data, err := os.ReadFile(idxPath); err == nil {
		_ = json.Unmarshal(data, &idx)
	}
	idx.Network = network
	if idx.Latest == nil {
		idx.Latest = make(map[string]*publishedRel)
	}
	rr := &publishedRel{
		Height:    raw.Height,
		Manifests: map[string]string{mode: raw.ManifestID},
		CreatedAt: raw.CreatedAt,
	}
	if cur, ok := idx.Latest[mode]; !ok || cur.Height < raw.Height {
		idx.Latest[mode] = rr
	}
	// Merge into Releases.
	var found bool
	for _, r := range idx.Releases {
		if r.Height == raw.Height {
			r.Manifests[mode] = raw.ManifestID
			found = true
			break
		}
	}
	if !found {
		idx.Releases = append(idx.Releases, rr)
	}
	f, err := os.Create(idxPath)
	if err != nil {
		t.Fatalf("create idx: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&idx); err != nil {
		t.Fatalf("encode idx: %v", err)
	}
}

type publishedRel struct {
	Height    uint64            `json:"height"`
	Manifests map[string]string `json:"manifests"`
	CreatedAt string            `json:"created_at"`
}

func fmtHeight(h uint64) string {
	const decimals = "0123456789"
	if h == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for h > 0 {
		i--
		b[i] = decimals[h%10]
		h /= 10
	}
	return string(b[i:])
}
