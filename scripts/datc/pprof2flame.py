#!/usr/bin/env python3
"""Render a Go CPU profile as a static SVG flame graph (no external tools).

usage: pprof2flame.py <profile.pb.gz> <out.svg> [title]
Uses `go tool pprof -traces` for the stacks; widths are CPU time.
"""
import html, re, subprocess, sys
from collections import defaultdict

prof, out = sys.argv[1], sys.argv[2]
title = sys.argv[3] if len(sys.argv) > 3 else prof
txt = subprocess.run(["go", "tool", "pprof", "-traces", prof], capture_output=True, text=True).stdout

def to_ns(v):
    m = re.fullmatch(r"([\d.]+)(ns|us|µs|ms|s|m|h)", v)
    if not m:
        return 0.0
    mult = {"ns": 1, "us": 1e3, "µs": 1e3, "ms": 1e6, "s": 1e9, "m": 60e9, "h": 3600e9}[m.group(2)]
    return float(m.group(1)) * mult

folded = defaultdict(float)
for block in txt.split("-----------+-------------------------------------------------------")[1:]:
    lines = [l for l in block.split("\n") if l.strip()]
    if not lines:
        continue
    first = lines[0].split()
    if len(first) < 2:
        continue
    w = to_ns(first[0])
    frames = [first[1]] + [l.strip() for l in lines[1:]]
    frames = [re.sub(r"^github\.com/n42blockchain/N42/", "", f) for f in frames]
    folded[";".join(reversed(frames))] += w

total = sum(folded.values())
tree = {"n": "all", "v": 0.0, "c": {}}
for stack, w in folded.items():
    node = tree
    node["v"] += w
    for fr in stack.split(";"):
        node = node["c"].setdefault(fr, {"n": fr, "v": 0.0, "c": {}})
        node["v"] += w

W, ROW = 1800, 17
rects, maxdepth = [], 0
def lay(node, x, depth):
    global maxdepth
    maxdepth = max(maxdepth, depth)
    width = node["v"] / total * W if total else 0
    rects.append((x, depth, width, node))
    cx = x
    for ch in sorted(node["c"].values(), key=lambda c: -c["v"]):
        lay(ch, cx, depth + 1)
        cx += ch["v"] / total * W
lay(tree, 0, 0)

def color(name):
    if name.startswith("runtime."):
        return "#e9a36b"
    if "mdbx" in name or name.startswith("_Cfunc"):
        return "#7fb3d5"
    if name.startswith("lib/trie") or "commitment" in name:
        return "#e57373"
    if name.startswith("main."):
        return "#81c784"
    h = sum(map(ord, name)) % 40
    return f"hsl({20 + h},70%,62%)"

H = (maxdepth + 3) * ROW + 30
svg = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" font-family="monospace" font-size="11">',
       f'<rect width="100%" height="100%" fill="#fdfaf5"/>',
       f'<text x="8" y="18" font-size="14">{html.escape(title)} — total {total/1e9:.1f} CPU-s (hover for details)</text>']
for x, depth, width, node in rects:
    if width < 0.3:
        continue
    y = H - (depth + 1) * ROW - 6
    pct = node["v"] / total * 100 if total else 0
    label = f'{node["n"]} ({node["v"]/1e9:.2f}s, {pct:.1f}%)'
    svg.append(f'<g><title>{html.escape(label)}</title><rect x="{x:.2f}" y="{y}" width="{width:.2f}" height="{ROW-1}" fill="{color(node["n"])}" stroke="#fff" stroke-width="0.3"/>')
    chars = int(width / 6.6)
    if chars > 3:
        t = node["n"] if len(node["n"]) <= chars else node["n"][:chars-2] + ".."
        svg.append(f'<text x="{x+3:.2f}" y="{y+12}">{html.escape(t)}</text>')
    svg.append('</g>')
svg.append('</svg>')
open(out, "w").write("\n".join(svg))
print(f"wrote {out}: {len(folded)} stacks, {total/1e9:.1f} CPU-s, depth {maxdepth}")
