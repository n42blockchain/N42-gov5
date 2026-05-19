# CS Warm Tier — Scheduler Runbook

**Date:** 2026-05-19
**Companions:**
- `archive-reduction-honest-targets.md` — sizes, what gets pruned
- `client-server-sync.md` — overall client/server architecture

This runbook covers operating the **periodic warm-tier rebuild** that
backs the live n42 node's fast Reorg path. The warm tier is a small
slice of the most-recent CS data; it must be refreshed periodically
so the unwind window stays current as the chain head advances.

---

## Scheduler model

The warm tier doesn't auto-rotate. The live node opens it read-only
on startup via `--cs-warm-dir` and keeps using the same dir. To
refresh, an external scheduler must:

1. Build a NEW warm-tier dir from the current freezer head
2. Atomically swap it into place
3. Cause the live node to re-open the dir

`cmd/n42-cs-prune` handles step 1 and 2 natively (`--swap` flag). Step
3 in v1 requires a node restart; v2 will add an admin reload endpoint.

---

## Three operating modes

### A. Manual one-shot

```bash
n42-cs-prune --src /data/n42/chain/freezer \
             --dst /data/n42/chain/freezer-warm \
             --keep-blocks 50400
```

Writes directly to `<dst>`. No atomic guarantee. Use only on a
stopped node, or for first-time bootstrap.

### B. Atomic swap (recommended for live ops)

```bash
n42-cs-prune --src /data/n42/chain/freezer \
             --dst /data/n42/chain/freezer-warm \
             --keep-blocks 50400 \
             --swap
```

Pipeline:
1. Writes new tier to `<dst>.staging/`
2. After successful build:
   - `rm -rf <dst>.old` (previous backup, if any)
   - `mv <dst> <dst>.old`
   - `mv <dst>.staging <dst>`
   - `rm -rf <dst>.old` (skip with `--keep-old`)

Filesystem-atomic per rename. Inconsistency window = a few ms between
`mv <dst> <dst>.old` and `mv <dst>.staging <dst>`. A node trying to
open during that window sees ENOENT — let it retry.

### C. Loop mode (built-in scheduler)

```bash
n42-cs-prune --src /data/n42/chain/freezer \
             --dst /data/n42/chain/freezer-warm \
             --keep-blocks 50400 \
             --swap --loop 168h
```

Loops forever. First cycle fires immediately, then waits `--loop`
duration between cycles. On prune failure, logs and tries again
next cycle (doesn't exit). Use with a process supervisor.

`--loop 168h` = weekly. Other common: `24h` daily, `6h` 4× daily.

---

## Live coordination: node restart cycle

After atomic swap, the running n42 node still has the OLD warm dir's
file handles open. Reads continue from the old data (Linux) or fail
(Windows file lock). New writes to the warm dir don't happen — the
freezer-warm is read-only inside the running node.

**Recommended ops pattern** (Linux/systemd):

```ini
# /etc/systemd/system/n42.service
[Service]
Restart=on-success
ExecStart=/usr/local/bin/n42 --data.dir /data/n42 \
    --cs-warm-dir /data/n42/chain/freezer-warm
```

```ini
# /etc/systemd/system/n42-cs-prune.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/n42-cs-prune \
    --src /data/n42/chain/freezer \
    --dst /data/n42/chain/freezer-warm \
    --keep-blocks 50400 \
    --swap
# After swap, request node restart so it picks up new warm.
ExecStartPost=/bin/systemctl restart n42
```

```ini
# /etc/systemd/system/n42-cs-prune.timer
[Unit]
Description=Weekly CS warm-tier rebuild

[Timer]
# Friday 00:00 UTC (chosen to align with server-side weekly publishes)
OnCalendar=Fri 00:00:00 UTC
Persistent=true

[Install]
WantedBy=timers.target
```

Enable with `systemctl enable --now n42-cs-prune.timer`.

Downtime per cycle: prune build ~60-90 s + node restart ~30 s = ~2 min.
Acceptable for archive nodes; for high-availability RPC, see "V2"
below.

---

## Cron (non-systemd)

```cron
# weekly Friday 00:00 UTC
0 0 * * 5 /usr/local/bin/n42-cs-prune \
    --src /data/n42/chain/freezer \
    --dst /data/n42/chain/freezer-warm \
    --keep-blocks 50400 \
    --swap && /bin/systemctl restart n42
```

---

## Kubernetes CronJob

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: n42-cs-prune
spec:
  schedule: "0 0 * * 5"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: prune
            image: n42:latest
            command: ["/usr/local/bin/n42-cs-prune"]
            args:
              - "--src=/data/chain/freezer"
              - "--dst=/data/chain/freezer-warm"
              - "--keep-blocks=50400"
              - "--swap"
            volumeMounts:
              - name: data
                mountPath: /data
          # After prune, restart the n42 pod via the operator
          # (sidecar that watches the prune Job and bounces the
          # main StatefulSet). Pattern intentionally explicit so
          # ops can tune restart strategy.
          restartPolicy: OnFailure
          volumes:
            - name: data
              persistentVolumeClaim:
                claimName: n42-data
```

---

## Operational verification

After each prune cycle, verify with `cmd/n42-cs-prune-verify`:

```bash
n42-cs-prune-verify --src /data/n42/chain/freezer \
                    --warm /data/n42/chain/freezer-warm \
                    --samples 1000
```

Expected:
- ✓ N samples match
- ✓ out-of-window blocks correctly rejected
- 0 mismatches, 0 missing

Add this as `ExecStartPost` in the systemd unit if you want belt-and-
suspenders verification before node restart.

---

## Tuning `--keep-blocks`

| keep-blocks | Time window | Warm size (real) | Use case |
|-------------|-------------|------------------|----------|
| 32 | ~6 min | <10 MB | Bare-minimum (PoS finality only) |
| 7,200 | 1 day | ~350 MB | Aggressive (daily prune cadence) |
| **50,400** | **7 days** | **2.4 GB** | **Default — weekly cadence** |
| 216,000 | 30 days | ~10 GB | Conservative (monthly cadence) |
| 1,000,000 | ~140 days | ~50 GB | Defeats purpose; just keep full freezer |

The window must exceed any reorg you want to handle without manual
recovery. Mainnet has never seen a reorg > 32 blocks since the Merge.
7 days = 1,575× that bound is plenty.

---

## Failure modes & recovery

| Failure | Symptom | Recovery |
|---------|---------|----------|
| Prune crashes mid-build | `<dst>.staging/` exists, `<dst>` untouched | Next cycle automatically cleans staging and retries |
| Disk full during build | Same as above | Free disk, retry |
| Swap step interrupted (e.g., kill -9 between rename steps) | `<dst>.old/` exists, `<dst>` may be missing | Manual: `mv <dst>.old <dst>`; or just re-run prune |
| Node reads stale warm after swap | Reorg uses old data | Restart node (or upgrade to v2 admin reload) |
| Source freezer truncated below warm window | Build succeeds but verify fails | Re-bootstrap from server snapshot — chaindata is corrupt |
| Wall clock drift causes double-fire | Two prune processes contending for `.staging/` | Use systemd's `OnFailure=ignore`; second process exits on `RemoveAll(.staging)` race |

---

## V2 roadmap (not yet implemented)

To eliminate the node-restart requirement:

1. **Admin RPC reload** — node exposes `POST /admin/cs/reload-warm`
   that does `csSource.Close()` + reopens. Scheduler calls this
   after swap.
2. **Hot-reload in cs.Warm** — internal goroutine watches `meta.json`
   mtime, atomically reopens freezer on change. No external signal
   needed.
3. **Generational warm dirs** — `freezer-warm.<epoch>/` numbered
   directories with `current` symlink. Node follows symlink on each
   read; no inode-stable handle, no reload needed. Linux-specific.

Approach 1 is simplest; 2 is most autonomous; 3 is most elegant but
non-portable. Pick based on deployment OS.

---

## Quick reference

| Command | Purpose |
|---------|---------|
| `n42-cs-prune --dry-run` | See projected sizes without writing |
| `n42-cs-prune --swap` | Build atomically, one-shot |
| `n42-cs-prune --swap --loop 168h` | Weekly self-scheduling |
| `n42-cs-prune-verify` | Round-trip check warm vs full freezer |
| `systemctl enable --now n42-cs-prune.timer` | Production schedule |
