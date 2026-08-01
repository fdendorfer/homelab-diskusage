# homelab-diskusage

A tiny Go web app for browsing disk usage (WinDirStat/TreeSize-style treemap) across one or more
directory roots. Built to run on-demand behind [Sablier](https://github.com/acouvreur/sablier) so
it only runs while someone is actually looking at it, rather than as an always-on background
indexer.

## How it works

- `main.go` — stdlib-only HTTP server. Walks a configured set of root directories concurrently,
  builds a nested `{name, size, children}` tree per root, and caches it in memory until the next
  rescan.
- `static/index.html` — single-page UI: pick a root, view a treemap or a sortable file-explorer
  list (toggle via "Show blocks"), click a directory to drill in, "Rescan" to refresh.
- Scanning is triggered explicitly (`POST /api/scan?root=<label>`) and runs asynchronously so a
  slow scan of a large tree doesn't hang the HTTP request or trip reverse-proxy timeouts; the
  frontend polls `/api/status` until it's done.

## Configuration

Set `ROOTS` as a comma-separated `label:path` list, e.g.:

```
ROOTS=system:/data/system,seagate:/data/seagate
```

Each path should be a read-only bind mount into the container (see `compose.yml`). A root can be
the whole host filesystem (bind-mount `/`) — scanning stays on that filesystem only (see below), so
it won't wander into other mounted drives or double-count them.

## API

- `GET  /api/roots` — configured scan roots
- `POST /api/scan?root=<label>` — start (or no-op if already running) a scan
- `GET  /api/status?root=<label>` — `{scanning, hasData, lastScan, error}`
- `GET  /api/data?root=<label>` — the cached tree for that root

## Notes / known limitations

- The treemap layout is a simple recursive binary-split, not a true squarified treemap — boxes are
  correctly sized but not optimally shaped for readability with many small siblings.
- Directory concurrency is bounded only around the `ReadDir` syscall itself (not around recursive
  descent) — earlier versions bounded the whole recursive call and could livelock on wide/deep
  trees, since a semaphore permit held while a goroutine blocks on its children's `wg.Wait()`
  starves those children of the permit they need to run.
- A fully-scanned tree stays resident in memory until the process exits; size accordingly
  (`mem_limit` in `compose.yml`) for how large your scanned roots actually are. Scanning a whole
  system root (as opposed to a single data directory) can easily need 1-2GB given enough files.
- Scanning stays on one filesystem, like `du -x`/`--one-file-system` (compares each directory's
  device ID against the scan root's). This matters a lot if a root bind-mounts the host's `/`:
  without it, the scan also descends into live `/proc`/`/sys`, and — worse — into Docker's own
  overlay "merged" mount for this very container, which recursively reflects the container's own
  view of itself. Both produced nonsense sizes (a single `/proc` entry reporting ~140TB from a
  stale `kcore`-style virtual file) before this was added.
