# eth-el datadir layout — chaindata/ subdir requirement

**Date:** 2026-05-23
**Status:** ⚠ Critical interop note. Affects anyone migrating from
`cmd/ethexec`-built MDBX into `cmd/eth-el`.

## TL;DR

`cmd/ethexec` writes MDBX at `<datadir>/mdbx.dat` (top level).
`cmd/eth-el` reads it at `<datadir>/chaindata/mdbx.dat`. If you
hand an ethexec-built datadir to eth-el unchanged, eth-el opens a
FRESH empty MDBX in `<datadir>/chaindata/` and the catch-up
service's alignOnResume sees freezer ahead of an empty MDBX and
**DISCARDS the freezer tail to "align"**.

This happened in our D:/N42-eth1177-test smoke launch — eth-el
silently nuked `acctcs.cidx` (25.1M items → 1 item) before we
killed the process at ~5s.

## Symptom in logs

```
eth-el: storage opened chaindata="D:\\N42-eth1177-test\\chaindata"
                       freezer="D:\\N42-eth1177-test\\chain\\freezer"
                       frozen=25101867
Catching up from=1 to=25101866 behind=25101865
alignOnResume starting startBlock=1
DISCARDING freezer tail — cdat ahead of MDBX
  discardedBlocks=25101866 freezerItems=25101867 mdbxProgress=1
  table=acctcs                                                  ← destructive
```

If you see `from=1` after pointing eth-el at a known-populated
ethexec datadir, **kill it immediately** — alignOnResume has
already begun the freezer truncation.

## Fix — move mdbx into chaindata/

```bash
mkdir <datadir>/chaindata
mv <datadir>/mdbx.dat <datadir>/chaindata/mdbx.dat
mv <datadir>/mdbx.lck <datadir>/chaindata/mdbx.lck   # optional
```

After the move, re-launch:

```
eth-el: storage opened chaindata="...\chaindata" freezer="...\chain\freezer" frozen=25101867
Already caught up frozen=25101867 head=25101866   ← correct path
eth-el: live mode entered
```

## Why two conventions?

`cmd/ethexec` predates `cmd/eth-el`. ethexec was the bring-up
tool; its --datadir flag points DIRECTLY at the MDBX dir.
cmd/eth-el wraps that plus the freezer + Caplin data dirs under
ONE root, so it makes a `chaindata/` subdir for the MDBX.

## Long-term fix options

1. **eth-el auto-detects** legacy top-level mdbx.dat and either
   moves it or opens it in-place (with a one-shot migration log).
2. **ethexec** writes to `<datadir>/chaindata/mdbx.dat` from
   day one. Breaks every existing datadir.
3. **Status quo + this doc** — operator runs the `mv` once when
   first handing an ethexec datadir to eth-el. Cheapest.

Recommend option 3 + add a startup check to eth-el that fails
LOUD when it sees a `<datadir>/mdbx.dat` at top level but its
chaindata subdir is empty — that's the actual destructive
scenario; printing a clear error is better than silently
nuking the freezer.

## Companion docs

- `docs/ethel/catchup-from-eth1177-recipe.md` — full catch-up recipe
- `docs/ethel/rebuild-state-resume-recipe.md` — RebuildState mechanics
- `memory/project_eth_el_bootstrap_paths.md` — mode bootstrap paths
