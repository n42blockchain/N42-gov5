// Package scenario implements the n42-eth-sim test harness.
// Exposed as a library so it's reachable from Go integration tests
// in addition to the CLI binary.
package scenario

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
)

// AllModes lists the three distribution modes in canonical order.
var AllModes = []string{"minimal", "full", "archive"}

// Scenario owns the publisher state, the published mirror dir,
// and one client per mode. All paths are under one root for easy
// cleanup.
type Scenario struct {
	root          string // <root>/
	archiveDir    string // <root>/publisher/archive   (single source of truth)
	publishedDir  string // <root>/published          (the "mirror")
	network       string

	mu     sync.Mutex
	height uint64

	// Per-mode state:
	prevManifestID map[string]string                // last published manifest_id per mode
	clients        map[string]*ClientState          // mode → client state
}

// ClientState tracks one simulated client's progress.
type ClientState struct {
	Datadir         string
	Mode            string
	Height          uint64
	LastDeltaFiles  int
	LastDeltaBytes  int64
	TotalBytes      int64
	Bootstrapped    bool
}

// Status is a snapshot of publisher + clients at a point in time.
type Status struct {
	PublisherHeight uint64
	Clients         map[string]*ClientState
}

// New initialises a fresh scenario under root. Does NOT publish
// anything yet; call PublisherTick + BootstrapClients to start.
func New(root string) (*Scenario, error) {
	s := &Scenario{
		root:           root,
		archiveDir:     filepath.Join(root, "publisher", "archive"),
		publishedDir:   filepath.Join(root, "published"),
		network:        "simnet",
		prevManifestID: make(map[string]string),
		clients:        make(map[string]*ClientState),
	}
	if err := os.MkdirAll(s.archiveDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.publishedDir, 0o755); err != nil {
		return nil, err
	}
	for _, mode := range AllModes {
		s.clients[mode] = &ClientState{
			Datadir: filepath.Join(root, "clients", mode),
			Mode:    mode,
		}
		if err := os.MkdirAll(s.clients[mode].Datadir, 0o755); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Scenario) PublishedDir() string { return s.publishedDir }

// PublisherTick grows the archive by 1000 blocks, regenerates all
// three manifests, builds deltas vs the previous tick (for ticks
// > 0), and publishes everything to the mirror.
func (s *Scenario) PublisherTick() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	first := s.height == 0
	s.height += 1000
	if err := s.growArchive(s.height); err != nil {
		return fmt.Errorf("grow archive: %w", err)
	}

	for _, mode := range AllModes {
		// 1. Capture previous manifest BEFORE overwriting.
		var prevMan *manifest.Manifest
		if !first {
			pm, err := snapshot.ManifestFor(s.archiveDir, mode)
			if err == nil {
				prevMan = pm
			}
		}

		// 2. Regenerate manifest for this mode.
		if err := writeManifest(s.archiveDir, mode, s.network, s.height); err != nil {
			return fmt.Errorf("write manifest %s: %w", mode, err)
		}
		curMan, err := snapshot.ManifestFor(s.archiveDir, mode)
		if err != nil {
			return fmt.Errorf("read new manifest %s: %w", mode, err)
		}

		// 3. Publish full release.
		if err := publishRelease(s.archiveDir, s.publishedDir, s.network, mode); err != nil {
			return fmt.Errorf("publish release %s: %w", mode, err)
		}

		// 4. Build + publish delta (if we have a previous one).
		if prevMan != nil && curMan.ManifestID != prevMan.ManifestID {
			if err := s.publishDelta(prevMan, curMan, mode); err != nil {
				return fmt.Errorf("publish delta %s: %w", mode, err)
			}
		}
		s.prevManifestID[mode] = curMan.ManifestID
	}
	return nil
}

// BootstrapClients runs the initial fetch for each client mode
// against the most recently published release.
func (s *Scenario) BootstrapClients() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mode := range AllModes {
		src := s.releaseSourceFor(mode, s.height)
		c := s.clients[mode]
		rep, err := snapshot.Fetch("file://"+src, c.Datadir, mode, false, false, 4)
		if err != nil {
			return fmt.Errorf("bootstrap %s: %w", mode, err)
		}
		if !rep.OK {
			return fmt.Errorf("bootstrap %s reported FAIL: %+v", mode, rep)
		}
		c.Height = s.height
		c.LastDeltaFiles = rep.Downloaded
		c.LastDeltaBytes = rep.BytesXfer
		c.TotalBytes = rep.BytesXfer
		c.Bootstrapped = true
	}
	return nil
}

// ClientApplyDelta applies the latest published delta for the
// given mode against the named client's datadir.
func (s *Scenario) ClientApplyDelta(mode string) error {
	s.mu.Lock()
	c := s.clients[mode]
	height := s.height
	s.mu.Unlock()
	if !c.Bootstrapped {
		return fmt.Errorf("client %s not bootstrapped", mode)
	}
	if c.Height >= height {
		c.LastDeltaFiles = 0
		c.LastDeltaBytes = 0
		return nil // already current
	}
	src := s.deltaSourceFor(mode, c.Height, height)
	rep, err := snapshot.ApplyDelta("file://"+src, c.Datadir, mode, 4)
	if err != nil {
		return fmt.Errorf("apply delta %s: %w", mode, err)
	}
	c.LastDeltaFiles = rep.Downloaded
	c.LastDeltaBytes = rep.BytesXfer
	c.TotalBytes += rep.BytesXfer
	c.Height = height
	return nil
}

// Status returns a copy of publisher + per-client state.
func (s *Scenario) Status() *Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &Status{
		PublisherHeight: s.height,
		Clients:         make(map[string]*ClientState, len(s.clients)),
	}
	for k, v := range s.clients {
		cp := *v
		st.Clients[k] = &cp
	}
	return st
}

// PrintFinalReport writes a per-mode summary including total bytes
// transferred and post-sim verify status.
func (s *Scenario) PrintFinalReport(w io.Writer) {
	st := s.Status()
	fmt.Fprintf(w, "publisher height : %d\n", st.PublisherHeight)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-10s  %-10s  %-14s  %-10s\n",
		"mode", "height", "total bytes", "matches?")
	fmt.Fprintf(w, "%-10s  %-10s  %-14s  %-10s\n",
		"----", "------", "-----------", "--------")
	for _, mode := range AllModes {
		c := st.Clients[mode]
		match := "OK"
		if c.Height != st.PublisherHeight {
			match = fmt.Sprintf("BEHIND by %d", st.PublisherHeight-c.Height)
		}
		fmt.Fprintf(w, "%-10s  %-10d  %-14d  %-10s\n",
			mode, c.Height, c.TotalBytes, match)
	}
}

// VerifyAllClients runs a full blake2b verify on each client's
// datadir and asserts the manifest_id matches the latest published
// manifest_id for that mode.
func (s *Scenario) VerifyAllClients() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mode := range AllModes {
		c := s.clients[mode]
		rep, err := snapshot.Verify(c.Datadir, "", 4)
		if err != nil {
			return fmt.Errorf("verify %s: %w", mode, err)
		}
		if !rep.OK {
			return fmt.Errorf("verify %s reported FAIL: missing=%d wrongSize=%d mismatches=%d",
				mode, len(rep.MissingFiles), len(rep.WrongSize), len(rep.Mismatches))
		}
		localMan, err := snapshot.ManifestFor(c.Datadir, mode)
		if err != nil {
			return fmt.Errorf("read manifest %s: %w", mode, err)
		}
		if localMan.ManifestID != s.prevManifestID[mode] {
			return fmt.Errorf("manifest_id mismatch %s: local=%s want=%s",
				mode, localMan.ManifestID, s.prevManifestID[mode])
		}
	}
	return nil
}

// --- internals ---

func (s *Scenario) releaseSourceFor(mode string, height uint64) string {
	return filepath.Join(s.publishedDir, s.network, fmt.Sprintf("%d", height), mode)
}

func (s *Scenario) deltaSourceFor(mode string, from, to uint64) string {
	return filepath.Join(s.publishedDir, s.network, "deltas",
		fmt.Sprintf("%d-%d", from, to), mode)
}

// growArchive adds fake "new block" data to the source archive.
// Per tick: the head .cdat for each table grows by 64 bytes,
// and the .cidx grows by one entry. Occasionally a new .cdat is
// rotated. Snapshot is regenerated only at segment boundaries
// (every 1M blocks); for the simulated growth rate, that's rare.
func (s *Scenario) growArchive(height uint64) error {
	// Make sure base files exist on first tick.
	if height == 1000 {
		if err := seedArchive(s.archiveDir); err != nil {
			return err
		}
		return nil
	}
	// Per-tick mutations: append a small byte to each freezer
	// table's head .cdat and its .cidx.
	tables := []string{
		"chain/freezer/headerc",
		"chain/freezer/bodyc",
		"chain/freezer/receipts",
		"chain/freezer/witness",
	}
	tickMark := []byte(fmt.Sprintf("\n#block-%d", height))
	for _, t := range tables {
		cdat := filepath.Join(s.archiveDir, t+".0000.cdat")
		cidx := filepath.Join(s.archiveDir, t+".cidx")
		if err := appendFile(cdat, tickMark); err != nil {
			return err
		}
		// cidx: mutate (overwrite with new content reflecting
		// updated entry count). 16B per entry.
		entries := 8 + int(height/1000)
		buf := make([]byte, entries*8)
		for i := range buf {
			buf[i] = byte(i + int(height%256))
		}
		if err := os.WriteFile(cidx, buf, 0o644); err != nil {
			return err
		}
	}
	// At 5K-block boundaries, rotate a new .cdat to exercise the
	// new-file path in the delta builder.
	if height%5000 == 0 {
		for _, t := range tables {
			nextNum := height / 5000
			newCdat := filepath.Join(s.archiveDir,
				fmt.Sprintf("%s.%04d.cdat", t, nextNum))
			content := []byte(fmt.Sprintf("rotated-segment-%d", height))
			if err := os.WriteFile(newCdat, content, 0o644); err != nil {
				return err
			}
		}
	}
	// At 1M-block boundaries, add a new snapshot segment.
	if height%1_000_000 == 0 {
		segStart := height - 999999
		segEnd := height
		for _, prefix := range []string{"accounts", "storage"} {
			for _, suffix := range []string{"idx", "ef", "val.zst"} {
				p := filepath.Join(s.archiveDir, "snapshot",
					fmt.Sprintf("%s.%d-%d.%s", prefix, segStart, segEnd, suffix))
				if err := os.WriteFile(p,
					[]byte(fmt.Sprintf("snap-seg-%s-%d-%d", prefix, segStart, segEnd)),
					0o644); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// seedArchive creates the minimum file set for height=1000.
func seedArchive(dir string) error {
	paths := []string{
		"chain/freezer/headerc.cidx",
		"chain/freezer/headerc.0000.cdat",
		"chain/freezer/codes.cidx",
		"chain/freezer/codes.0000.cdat",
		"chain/freezer/bodyc.cidx",
		"chain/freezer/bodyc.0000.cdat",
		"chain/freezer/receipts.cidx",
		"chain/freezer/receipts.0000.cdat",
		"chain/freezer/witness.cidx",
		"chain/freezer/witness.0000.cdat",
		"chain/freezer/accthist.cidx",
		"chain/freezer/accthist.0000.cdat",
		"chain/freezer/storhist.cidx",
		"chain/freezer/storhist.0000.cdat",
		"chain/freezer/txindex.cidx",
		"chain/freezer/txindex.0000.cdat",
		"snapshot/accounts.0-999999.idx",
		"snapshot/accounts.0-999999.ef",
		"snapshot/accounts.0-999999.val.zst",
		"snapshot/storage.0-999999.idx",
		"snapshot/storage.0-999999.ef",
		"snapshot/storage.0-999999.val.zst",
	}
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte("seed-"+p), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func writeManifest(archDir, mode, network string, height uint64) error {
	sel, err := manifest.SelectorFor(mode)
	if err != nil {
		return err
	}
	files, err := manifest.WalkFiles(archDir, sel)
	if err != nil {
		return err
	}
	for _, f := range files {
		h, err := snapshot.HashFile(filepath.Join(archDir, f.Path))
		if err != nil {
			return err
		}
		f.Blake2b256 = h
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\t%d\t%s\n", f.Path, f.Size, f.Blake2b256)
	}
	m := &manifest.Manifest{
		Network:    network,
		Height:     height,
		Mode:       mode,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		ManifestID: hex.EncodeToString(h.Sum(nil)),
		Files:      files,
	}
	return writeJSON(filepath.Join(archDir, "manifest-"+mode+".json"), m)
}

func publishRelease(archDir, publishedDir, network, mode string) error {
	man, err := snapshot.ManifestFor(archDir, mode)
	if err != nil {
		return err
	}
	dst := filepath.Join(publishedDir, network, fmt.Sprintf("%d", man.Height), mode)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, f := range man.Files {
		if err := copyFile(
			filepath.Join(archDir, f.Path),
			filepath.Join(dst, f.Path)); err != nil {
			return err
		}
	}
	return copyFile(
		filepath.Join(archDir, "manifest-"+mode+".json"),
		filepath.Join(dst, "manifest-"+mode+".json"),
	)
}

func (s *Scenario) publishDelta(prev, cur *manifest.Manifest, mode string) error {
	prevIdx := make(map[string]string, len(prev.Files))
	for _, f := range prev.Files {
		prevIdx[f.Path] = f.Blake2b256
	}
	var deltaFiles []*manifest.FileEntry
	for _, f := range cur.Files {
		if h, ok := prevIdx[f.Path]; ok && h == f.Blake2b256 {
			continue
		}
		deltaFiles = append(deltaFiles, f)
	}
	if len(deltaFiles) == 0 {
		return nil
	}
	sort.Slice(deltaFiles, func(i, j int) bool {
		return deltaFiles[i].Path < deltaFiles[j].Path
	})
	dst := filepath.Join(s.publishedDir, s.network, "deltas",
		fmt.Sprintf("%d-%d", prev.Height, cur.Height), mode)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, f := range deltaFiles {
		if err := copyFile(
			filepath.Join(s.archiveDir, f.Path),
			filepath.Join(dst, f.Path)); err != nil {
			return err
		}
	}
	dm := &snapshot.DeltaManifest{
		Network:           cur.Network,
		FromHeight:        prev.Height,
		ToHeight:          cur.Height,
		Mode:              mode,
		BasedOnManifestID: prev.ManifestID,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
		ManifestID:        cur.ManifestID,
		Files:             deltaFiles,
	}
	if err := writeJSON(filepath.Join(dst, "delta-manifest-"+mode+".json"), dm); err != nil {
		return err
	}
	// ApplyDelta also fetches the new full manifest from the same
	// source, so put a copy in the delta dir.
	return copyFile(
		filepath.Join(s.archiveDir, "manifest-"+mode+".json"),
		filepath.Join(dst, "manifest-"+mode+".json"),
	)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func writeJSON(path string, v any) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
