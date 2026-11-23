// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Rollout switch and limits of the parallel catch-up pipeline (sync v2
// design S2 + S5, N42 sync standard §3.4 / §6).
//
// N42_SYNC_PIPELINE=1 enables both:
//
//   - S2: the range catch-up fetches slots of the gap from several peers in
//     parallel into a bounded reorder buffer while a single importer executes
//     the in-order stream (fetch overlaps execution);
//   - S5: explicit Live / Range sync modes with hysteresis; in Range the
//     leader does not produce and the live ingress paths leave heights up to
//     the range end to the sync importer.
//
// Default OFF: with the switch off every catch-up, ingress and production
// path behaves exactly as before. No wire change either way (v1
// bodies_by_range only).

package sync

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/n42blockchain/N42/log"
)

// pipelineEnvVar enables S2 + S5.
const pipelineEnvVar = "N42_SYNC_PIPELINE"

var pipelineOn atomic.Bool

// pipelineConfig holds the tunables of one pipeline run. Defaults follow
// the v2 design (§4) and the standard's quotas (§6.3: <= 2 range streams per
// serving peer).
type pipelineConfig struct {
	// slotBlocks caps the blocks of one request (one slot).
	slotBlocks uint64
	// slotBytes sizes a slot from the measured block size:
	// count = clamp(slotBytes / ewmaBlockBytes, 1, slotBlocks).
	slotBytes uint64
	// bufferBlocks / bufferBytes bound the reorder buffer plus the bytes
	// reserved by requests in flight (back-pressure).
	bufferBlocks int
	bufferBytes  uint64
	// peerInflight is the most requests one peer may have in flight. The
	// adaptive per-peer limit starts at 1 and grows to this.
	peerInflight int
}

var pipelineCfg = loadPipelineConfig()

func defaultPipelineConfig() pipelineConfig {
	return pipelineConfig{
		slotBlocks:   64,
		slotBytes:    64 << 20,
		bufferBlocks: 256,
		bufferBytes:  2 << 30,
		peerInflight: 2,
	}
}

func envUint(name string, def uint64, lo, hi uint64) uint64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n < lo || n > hi {
		log.Warn("sync pipeline: ignoring an invalid setting", "env", name, "value", v, "default", def)
		return def
	}
	return n
}

func loadPipelineConfig() pipelineConfig {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(pipelineEnvVar))) {
	case "1", "true", "yes", "on":
		pipelineOn.Store(true)
	}
	c := defaultPipelineConfig()
	c.slotBlocks = envUint("N42_SYNC_PIPELINE_SLOT_BLOCKS", c.slotBlocks, 1, maxRequestBlocks)
	c.slotBytes = envUint("N42_SYNC_PIPELINE_SLOT_MB", c.slotBytes>>20, 1, 128) << 20
	c.bufferBlocks = int(envUint("N42_SYNC_PIPELINE_BUFFER_BLOCKS", uint64(c.bufferBlocks), 8, 1<<16))
	c.bufferBytes = envUint("N42_SYNC_PIPELINE_BUFFER_MB", c.bufferBytes>>20, 64, 1<<20) << 20
	c.peerInflight = int(envUint("N42_SYNC_PIPELINE_PEER_INFLIGHT", uint64(c.peerInflight), 1, 4))
	if pipelineOn.Load() {
		log.Info("sync pipeline enabled (S2 parallel range fetch + S5 sync modes)",
			"env", pipelineEnvVar, "slotBlocks", c.slotBlocks, "slotMB", c.slotBytes>>20,
			"bufferBlocks", c.bufferBlocks, "bufferMB", c.bufferBytes>>20, "peerInflight", c.peerInflight)
	}
	return c
}

// PipelineOn reports whether N42_SYNC_PIPELINE is enabled.
func PipelineOn() bool { return pipelineOn.Load() }

// setPipelineForTest overrides the switch and returns a restore function.
func setPipelineForTest(on bool) (restore func()) {
	prev := pipelineOn.Swap(on)
	return func() { pipelineOn.Store(prev) }
}
