#!/usr/bin/env python3
"""Resource monitor for the qs fleet and the Rust mixed fleets on this box.

Samples every process group for --seconds and prints, per group and per
process: CPU (cores), RSS, threads, fds, disk read/write (from /proc/<pid>/io,
actual block I/O), TCP bytes in/out (from `ss -tinp`, loopback included; QUIC
(UDP) traffic is only visible in the system loopback counters), plus system
load, memory, NIC counters, NVMe device I/O (iostat) and temperatures from
/sys/class/hwmon (CPU Tctl via k10temp, NVMe controllers, NICs) and smartctl
when readable. No root needed for anything but smartctl.

usage: fleet-monitor.py [--seconds 60] [--interval 5] [--json out.json]
"""
import argparse, glob, json, os, re, subprocess, time

GROUPS = [
    ("qs-fleet (7 Go validators, chain 94)", lambda a: "--data.dir /data/blockchain/qs-node" in a),
    ("n42-rs EL (reth+QMDB)",                lambda a: "n42-rs/target/debug/n42 node" in a),
    ("n42-rs validators (h2_validator)",     lambda a: "examples/h2_validator" in a),
    ("n42-rs devnet gov5 member",            lambda a: "wt-main/build/bin/n42 " in a),
    ("N42-26 Rust validator (n42-node)",     lambda a: "N42-26/target/release/n42-node" in a),
    ("N42-26 devnet Go validators",          lambda a: "runtime/geth-live" in a),
]
CLK = os.sysconf("SC_CLK_TCK")

def read(p):
    try:
        with open(p) as f: return f.read()
    except Exception: return ""

def procs():
    out = {}
    for d in glob.glob("/proc/[0-9]*"):
        pid = int(d[6:])
        args = read(f"{d}/cmdline").replace("\0", " ")
        if not args: continue
        for name, match in GROUPS:
            if match(args):
                out[pid] = name; break
    return out

def cpu_ticks(pid):
    s = read(f"/proc/{pid}/stat")
    if not s: return None
    f = s[s.rindex(")")+2:].split()
    return int(f[11]) + int(f[12])  # utime+stime

def rss_kb(pid):
    for line in read(f"/proc/{pid}/status").splitlines():
        if line.startswith("VmRSS:"): return int(line.split()[1])
    return 0

def threads(pid):
    for line in read(f"/proc/{pid}/status").splitlines():
        if line.startswith("Threads:"): return int(line.split()[1])
    return 0

def fds(pid):
    try: return len(os.listdir(f"/proc/{pid}/fd"))
    except Exception: return 0

def pio(pid):
    r = {}
    for line in read(f"/proc/{pid}/io").splitlines():
        k, _, v = line.partition(":"); r[k.strip()] = int(v)
    return r

def tcp_bytes():
    """{pid: (sent, received)} summed over TCP sockets, via ss -tinp."""
    try:
        txt = subprocess.run(["ss", "-tinp"], capture_output=True, text=True, timeout=10).stdout
    except Exception: return {}
    res, pid = {}, None
    for line in txt.splitlines():
        m = re.search(r"pid=(\d+)", line)
        if m: pid = int(m.group(1)); continue
        if pid is None: continue
        s = re.search(r"bytes_sent:(\d+)", line); r = re.search(r"bytes_received:(\d+)", line)
        if s or r:
            a, b = res.get(pid, (0, 0))
            res[pid] = (a + (int(s.group(1)) if s else 0), b + (int(r.group(1)) if r else 0))
        pid = None
    return res

def netdev():
    r = {}
    for line in read("/proc/net/dev").splitlines()[2:]:
        name, _, rest = line.partition(":"); f = rest.split()
        r[name.strip()] = (int(f[0]), int(f[8]))  # rx, tx bytes
    return r

def temps():
    out = []
    for h in sorted(glob.glob("/sys/class/hwmon/hwmon*")):
        name = read(f"{h}/name").strip()
        for t in sorted(glob.glob(f"{h}/temp*_input")):
            label = read(t.replace("_input", "_label")).strip() or os.path.basename(t)[:-6]
            try: out.append((name, label, int(read(t)) / 1000))
            except Exception: pass
    return out

def smart(dev):
    try:
        txt = subprocess.run(["smartctl", "-a", dev], capture_output=True, text=True, timeout=10).stdout
    except Exception: return {}
    r = {}
    for line in txt.splitlines():
        for key in ("Temperature:", "Percentage Used:", "Data Units Read:", "Data Units Written:", "Available Spare:", "Critical Warning:"):
            if line.strip().startswith(key): r[key[:-1]] = line.split(":", 1)[1].strip()
    return r

def iostat_unused(seconds):
    try:
        txt = subprocess.run(["iostat", "-dx", "-k", str(seconds), "1"], capture_output=True, text=True, timeout=seconds + 15).stdout
    except Exception: return {}
    res, cols = {}, None
    for line in txt.splitlines():
        f = line.split()
        if not f: continue
        if f[0] == "Device": cols = f; continue
        if cols and f[0].startswith("nvme") and len(f) == len(cols):
            res[f[0]] = dict(zip(cols[1:], f[1:]))
    return res

def fmt_bytes(b):
    for u in ("B", "KB", "MB", "GB", "TB"):
        if abs(b) < 1024: return f"{b:.1f}{u}"
        b /= 1024
    return f"{b:.1f}PB"

def main():
    ap = argparse.ArgumentParser(); ap.add_argument("--seconds", type=int, default=60)
    ap.add_argument("--interval", type=int, default=5); ap.add_argument("--json")
    a = ap.parse_args()
    groups = procs()
    if not groups: print("no fleet processes found"); return
    pids = sorted(groups)
    t0 = time.time(); c0 = {p: cpu_ticks(p) for p in pids}; io0 = {p: pio(p) for p in pids}; n0 = tcp_bytes(); nd0 = netdev()
    peak_rss = {p: 0 for p in pids}; cpu_samples = {p: [] for p in pids}; prev = dict(c0); tprev = t0
    # iostat runs for the whole window in the background
    ios = subprocess.Popen(["iostat", "-dx", "-k", str(max(1, a.seconds - 1)), "2"], stdout=subprocess.PIPE, text=True)  # report 1 = since boot, report 2 = the window
    end = t0 + a.seconds
    while time.time() < end:
        time.sleep(min(a.interval, max(0.1, end - time.time())))
        now = time.time()
        for p in pids:
            c = cpu_ticks(p)
            if c is None or prev[p] is None: continue
            cpu_samples[p].append((c - prev[p]) / CLK / (now - tprev)); prev[p] = c
            peak_rss[p] = max(peak_rss[p], rss_kb(p))
        tprev = now
    el = time.time() - t0
    c1 = {p: cpu_ticks(p) for p in pids}; io1 = {p: pio(p) for p in pids}; n1 = tcp_bytes(); nd1 = netdev()
    iotxt = ios.communicate()[0]
    rows = []
    for p in pids:
        if c1[p] is None or c0[p] is None: continue
        cores = (c1[p] - c0[p]) / CLK / el
        rd = io1[p].get("read_bytes", 0) - io0[p].get("read_bytes", 0); wr = io1[p].get("write_bytes", 0) - io0[p].get("write_bytes", 0)
        tx = n1.get(p, (0, 0))[0] - n0.get(p, (0, 0))[0]; rx = n1.get(p, (0, 0))[1] - n0.get(p, (0, 0))[1]
        rows.append(dict(pid=p, group=groups[p], cores=cores, cores_peak=max(cpu_samples[p] or [0]), rss_kb=rss_kb(p), peak_rss_kb=peak_rss[p],
                         threads=threads(p), fds=fds(p), disk_read=rd, disk_write=wr, tcp_tx=tx, tcp_rx=rx,
                         args=read(f"/proc/{p}/cmdline").replace("\0", " ")[:90]))
    print(f"=== fleet monitor: {el:.0f}s window, {len(rows)} processes, {time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime())} ===")
    print(f"{'group':38s} {'procs':>5s} {'cores':>7s} {'peak':>6s} {'RSS':>9s} {'peakRSS':>9s} {'thr':>5s} {'fds':>5s} {'disk rd/s':>10s} {'disk wr/s':>10s} {'tcp tx/s':>10s} {'tcp rx/s':>10s}")
    for name, _ in GROUPS:
        g = [r for r in rows if r["group"] == name]
        if not g: continue
        print(f"{name[:38]:38s} {len(g):5d} {sum(r['cores'] for r in g):7.2f} {max(r['cores_peak'] for r in g):6.2f} {fmt_bytes(sum(r['rss_kb'] for r in g)*1024):>9s} {fmt_bytes(sum(r['peak_rss_kb'] for r in g)*1024):>9s} {sum(r['threads'] for r in g):5d} {sum(r['fds'] for r in g):5d} {fmt_bytes(sum(r['disk_read'] for r in g)/el):>9s}/s {fmt_bytes(sum(r['disk_write'] for r in g)/el):>9s}/s {fmt_bytes(sum(r['tcp_tx'] for r in g)/el):>9s}/s {fmt_bytes(sum(r['tcp_rx'] for r in g)/el):>9s}/s")
    print("\n--- per process ---")
    for r in sorted(rows, key=lambda r: -r["cores"]):
        print(f"pid {r['pid']:>8d} {r['cores']:5.2f} cores (peak {r['cores_peak']:4.2f}) RSS {fmt_bytes(r['rss_kb']*1024):>8s} thr {r['threads']:4d} fds {r['fds']:4d} disk {fmt_bytes(r['disk_read']/el):>8s}/s rd {fmt_bytes(r['disk_write']/el):>8s}/s wr tcp {fmt_bytes(r['tcp_tx']/el):>8s}/s tx {fmt_bytes(r['tcp_rx']/el):>8s}/s rx | {r['args']}")
    load = read("/proc/loadavg").split()[:3]
    mem = {l.split(":")[0]: int(l.split()[1]) for l in read("/proc/meminfo").splitlines() if ":" in l}
    print(f"\n--- system --- load {load} | mem total {fmt_bytes(mem['MemTotal']*1024)} used {fmt_bytes((mem['MemTotal']-mem['MemAvailable'])*1024)} available {fmt_bytes(mem['MemAvailable']*1024)} | cpus {os.cpu_count()}")
    for dev in ("lo",) + tuple(k for k in nd1 if k not in ("lo",) and nd1[k][0] != nd0.get(k, (0, 0))[0]):
        rx = nd1[dev][0] - nd0.get(dev, (0, 0))[0]; tx = nd1[dev][1] - nd0.get(dev, (0, 0))[1]
        print(f"nic {dev:8s} rx {fmt_bytes(rx/el):>9s}/s tx {fmt_bytes(tx/el):>9s}/s")
    cols, report = None, 0
    for line in iotxt.splitlines():
        f = line.split()
        if not f: continue
        if f[0] == "Device": cols = f; report += 1; continue
        if report == 2 and cols and f[0].startswith("nvme") and len(f) == len(cols):
            d = dict(zip(cols[1:], f[1:]))
            print(f"nvme {f[0]:9s} r {d.get('r/s','?'):>8s}/s {d.get('rkB/s','?'):>10s} kB/s  w {d.get('w/s','?'):>8s}/s {d.get('wkB/s','?'):>10s} kB/s  await r/w {d.get('r_await','?')}/{d.get('w_await','?')} ms  util {d.get('%util','?')}%")
    for pw in glob.glob("/sys/class/hwmon/hwmon*/power1_input"):
        try: print(f"cpu package power (amd_hsmp) {int(read(pw))/1e6:.1f} W")
        except Exception: pass
    print("--- temperatures (hwmon; no board/VRM sensor is exposed on this H14SSL-NT without IPMI or a Super-I/O driver) ---")
    for name, label, t in temps():
        print(f"{name:14s} {label:12s} {t:6.1f} C")
    for dev in ("/dev/nvme0n1", "/dev/nvme1n1"):
        s = smart(dev)
        if s: print(f"smartctl {dev}: " + ", ".join(f"{k}={v}" for k, v in s.items()))
    if a.json:
        json.dump(dict(window_s=el, rows=rows, load=load, temps=temps(), mem=mem), open(a.json, "w"), indent=1)

if __name__ == "__main__":
    main()
