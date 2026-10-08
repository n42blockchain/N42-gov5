#!/usr/bin/env python3
"""Ship the weekly DATC inputs (acctcs/storcs/headerc) to n42dev.

Encodes the transfer rules from docs/ethel/datc-weekly-update.md section 4 so they
no longer depend on a human remembering them:

  1. A table's tail segment is not append-only (the last under-full batch is
     re-encoded in place), so the tail .cdat and the .cidx are replaced WHOLE
     FILE: upload to a staging directory, verify, then atomic rename over the
     destination. Never append, never overwrite in place.
  2. Order: new segments first, then the tail .cdat, and the .cidx LAST. A reader
     that sees the new cidx must find every segment it points at.
  3. Never write through into the shared witness directory. headerc.NNNN.cdat on
     the remote may be symlinks into it; only the changed tail .cdat and .cidx are
     touched, and they are replaced by renaming a real file over the link, which
     replaces the link itself and leaves the target alone.

Sealed segments (anything below the remote tail) must be byte-for-byte the same
size on both sides; a mismatch aborts, because it means the two sides are not the
same chain. Default is a dry run; pass --apply to transfer.

Run with: python -I scripts/datc/ship-inputs.py --tip 26130711 [--apply]
"""
import argparse
import hashlib
import os
import re
import subprocess
import sys

HOST = "n42@192.168.0.166"
REMOTE_ROOT = "/data/blockchain"
WITNESS = REMOTE_ROOT + "/witness"
SEG = re.compile(r"^(?P<t>[a-z]+)\.(?P<n>\d{4})\.cdat$")

# dataset -> (local freezer dir, tables)
DATASETS = {
    "N42-eth1177": ("D:/N42-eth1177/chain/freezer", ["acctcs", "storcs"]),
    "n42-eth1": ("D:/n42-eth1/chain/freezer", ["headerc"]),
}


def ssh(cmd, check=True):
    p = subprocess.run(["ssh", "-o", "BatchMode=yes", HOST, cmd], capture_output=True, text=True)
    if check and p.returncode != 0:
        sys.exit("ssh failed (%d): %s\n%s" % (p.returncode, cmd, p.stderr.strip()))
    return p.stdout


def sha256_local(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while True:
            b = f.read(8 << 20)
            if not b:
                break
            h.update(b)
    return h.hexdigest()


def sha256_remote(path):
    out = ssh("sha256sum %s" % path, check=False).split()
    return out[0] if out else ""


def remote_listing(rdir, table):
    """name -> (size, is_symlink) for every file of one table."""
    cmd = (
        'cd %s && for f in %s.*; do [ -e "$f" ] || continue; '
        'printf "%%s %%s %%s\\n" "$f" "$(stat -L -c %%s "$f")" "$([ -L "$f" ] && echo L || echo F)"; done'
        % (rdir, table)
    )
    res = {}
    for line in ssh(cmd).splitlines():
        name, size, kind = line.split()
        res[name] = (int(size), kind == "L")
    return res


def plan_table(ldir, rdir, table):
    local = sorted(f for f in os.listdir(ldir) if f.startswith(table + ".") and f != table + ".cidx"
                   and SEG.match(f))
    remote = remote_listing(rdir, table)
    rsegs = sorted(n for n in remote if SEG.match(n))
    if not rsegs:
        sys.exit("%s: no remote segments under %s" % (table, rdir))
    rtail = rsegs[-1]
    missing_locally = [n for n in rsegs if n not in local]
    if missing_locally:
        sys.exit("%s: remote has segments the local side lacks: %s" % (table, missing_locally))
    ship = []
    for n in local:
        lsize = os.path.getsize(os.path.join(ldir, n))
        if n not in remote:
            ship.append((n, "new", lsize))
        elif n == rtail:
            ship.append((n, "tail", lsize))
        elif remote[n][0] != lsize:
            sys.exit("%s: sealed segment %s differs (local %d, remote %d); not the same chain, refusing"
                     % (table, n, lsize, remote[n][0]))
    cidx = table + ".cidx"
    ship.append((cidx, "cidx", os.path.getsize(os.path.join(ldir, cidx))))
    # rule 2: new segments, then tail, cidx last (list is already in that order)
    order = {"new": 0, "tail": 1, "cidx": 2}
    ship.sort(key=lambda s: (order[s[1]], s[0]))
    return ship, remote


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tip", type=int, required=True, help="target block, used to name the staging/backup dirs")
    ap.add_argument("--apply", action="store_true", help="transfer; without it only the plan is printed")
    a = ap.parse_args()

    stage_root = "%s/datc-input-staging-%d" % (REMOTE_ROOT, a.tip)
    backup_root = "%s/datc-input-backup-%d" % (REMOTE_ROOT, a.tip)

    plans = []
    total = 0
    for ds, (ldir, tables) in DATASETS.items():
        rdir = "%s/datc-input/%s/chain/freezer" % (REMOTE_ROOT, ds)
        if rdir.startswith(WITNESS):
            sys.exit("refusing to operate under the shared witness directory")
        for t in tables:
            ship, remote = plan_table(ldir, rdir, t)
            for n, kind, size in ship:
                total += size
                print("%-12s %-8s %-6s %8.1f MB" % (ds, n, kind, size / 1e6))
            plans.append((ds, ldir, rdir, t, ship, remote))

    free = int(ssh("df -B1 --output=avail %s | tail -1" % REMOTE_ROOT).strip())
    print("to ship %.2f GB, remote free %.1f GB" % (total / 1e9, free / 1e9))
    if free < 2 * total + (20 << 30):
        sys.exit("not enough free space on the remote (need 2x payload + 20 GB)")
    if not a.apply:
        print("dry run; re-run with --apply")
        return

    # stage everything first, verified by sha256; nothing live is touched yet
    staged = []
    for ds, ldir, rdir, t, ship, remote in plans:
        sdir = "%s/%s" % (stage_root, ds)
        ssh("mkdir -p %s %s/%s" % (sdir, backup_root, ds))
        for n, kind, size in ship:
            lpath = os.path.join(ldir, n)
            want = sha256_local(lpath)
            dest = "%s/%s" % (rdir, n)
            if kind in ("tail", "cidx") and sha256_remote(dest) == want:
                print("unchanged on remote, skipped: %s/%s" % (ds, n))
                continue
            spath = "%s/%s" % (sdir, n)
            if sha256_remote(spath) != want:
                r = subprocess.run(["scp", "-q", "-o", "BatchMode=yes", lpath, "%s:%s.part" % (HOST, spath)])
                if r.returncode != 0:
                    sys.exit("scp failed for %s" % n)
                ssh("mv -f %s.part %s" % (spath, spath))
                if sha256_remote(spath) != want:
                    sys.exit("staged copy of %s does not match the local sha256" % n)
            print("staged %s/%s %s" % (ds, n, want[:12]))
            staged.append((ds, rdir, spath, dest, n, want))

    # promote in plan order (new, tail, cidx last); each step is an atomic rename
    for ds, rdir, spath, dest, n, want in staged:
        bpath = "%s/%s/%s" % (backup_root, ds, n)
        # keep the old inode (or the link itself) reachable before it is replaced
        ssh('if [ -L %s ]; then cp -P %s %s; elif [ -e %s ]; then ln -f %s %s; fi'
            % (dest, dest, bpath, dest, dest, bpath))
        ssh("mv -f -T %s %s" % (spath, dest))
        if sha256_remote(dest) != want:
            sys.exit("post-promote check failed for %s" % dest)
        print("promoted %s/%s" % (ds, n))
    print("done; backups in %s, staging %s (remove both after the DATC weekly passes verify)" % (backup_root, stage_root))


if __name__ == "__main__":
    main()
