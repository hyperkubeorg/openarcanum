# Load tests

Targets:

| Server | Target |
|---|---|
| Community server | 5,000 members, 500 online, 50 messages/s sustained, on 2 vCPU / 4 GB |
| Relay | 2,000 concurrent connections |
| Maintainer node | 10,000 directory listings, search p95 under 200 ms |

## How they run

`make load` (or `LOAD_RUN=TestRelay make load` for one) runs
`internal/loadtest` (build tag `load`, so `make check` never does):

- Each server is its **real binary** (`make build`), started under
  `taskset -c $LOAD_SERVER_CPUS` (default `0,1`) with `GOMAXPROCS` set to the
  same count, as on a 2-vCPU machine. The community test's relay runs on
  `LOAD_RELAY_CPUS` (default `2,3`) and the test's clients on
  `LOAD_CLIENT_CPUS` (default `4-25`), so they don't take the server's CPU.
- CPU is the server process's user+system time over the measured window
  (`/proc/PID/stat`), as cores used; memory is its resident set and peak
  (`VmRSS`, `VmHWM`). Memory isn't capped during the run; the test fails if
  the peak passes the target's 4 GiB.
- Results are printed as one JSON line per test and appended to
  `LOAD_RESULTS` (`load-results.jsonl`).
- Sizes and durations can be changed without editing the tests:
  `LOAD_MEMBERS`, `LOAD_ONLINE`, `LOAD_RATE`, `LOAD_DURATION`,
  `LOAD_JOIN_CONCURRENCY`, `LOAD_CONNECTIONS`, `LOAD_LISTINGS`,
  `LOAD_SEARCHERS`; `LOAD_LOG_DIR` keeps the servers' logs.

What each test does:

- **Community server** (`TestCommunityServer`): `community init` + `run`, a
  relay, the owner claims, then `LOAD_MEMBERS` members connect and join with
  one invite (64 at a time). The first `LOAD_ONLINE` stay connected and watch
  `#general` over the relay; 25 of them decrypt and verify every message to
  time delivery, the rest count deliveries. Members post `LOAD_RATE`
  messages a second in turn for `LOAD_DURATION` (5 minutes). Pass: no failed
  posts, every watcher gets every message, post round trip p95 ≤ 500 ms,
  delivery p95 ≤ 1 s, peak memory ≤ 4 GiB.
- **Relay** (`TestRelay`): `LOAD_CONNECTIONS` WebSocket connections, each
  subscribed to one of `LOAD_CONNECTIONS/20` topics; one connection per topic
  publishes 1 KiB every second (19 deliveries each) for `LOAD_DURATION`
  (2 minutes); the rest only keep their connection alive. Pass: no
  connection dropped, every delivery arrives, delivery p95 ≤ 250 ms.
- **Maintainer node** (`TestMaintainer`): `LOAD_LISTINGS` owner-signed
  listings posted through the API (the per-address write and read limits raised,
  since every listing comes from one address), then `LOAD_SEARCHERS` (16)
  clients search back to back for `LOAD_DURATION` (1 minute), mixing a word,
  a tag, both, and browsing. Pass: search p95 < 200 ms, no errors, no empty
  results (every word and tag is used by many listings).

## Problems the load tests found

- **Presence flooded the server's relay connection.** Every member's
  sessions reach a community server through its one relay connection, and
  each member coming online or going offline was sent to every online
  session as its own message. With 5 members online while 200 joined, joins
  took p95 3.9 s (the relay paused the connection at its frame budget); at
  500 online, a reconnect storm after a relay restart would have been about
  250,000 messages. Presence is now batched: one `PresenceBatch` per session
  every 500 ms (docs/protocol.md "Presence"). Same test: p95 133 ms.
- **Directory search scanned every listing.** Searching by tag and browsing
  sorted all listings for each request: at 10,000 listings, p95 218 ms at
  231 searches/s. Two indexes (maintainer migration 5): p95 36 ms at
  1,329 searches/s.
- **Relay frame budget for hosting connections.** A community server's
  relay connection carries all of its members' traffic but had a single
  client's frame budget, so the relay paused it: 5,000 joins with 500 online
  took 11 minutes (connect p50 6.5 s) and the first seconds of posting after
  that were slow (p99 1.9 s). Connections that host an endpoint now get a
  budget per open stream, capped (docs/operations.md "Abuse controls"):
  the same joins take 17 s.

## Results

Recorded 2026-10-24 with `make load` at the targets' full size, on one
Linux machine (32 cores): each server binary pinned to 2 cores
(`LOAD_SERVER_CPUS=0,1`, `GOMAXPROCS=2`), the community test's relay on
cores 2-3, clients on 4-25. Everything ran over loopback, so the numbers
show the servers' own cost, not network latency. Other work on the machine
during the runs was light (load average under 4). Raw JSON:
`load-results.jsonl` from that run, reproduced below in short.

| Server | Target | Result | Pass |
|---|---|---|---|
| Community server | 5,000 members, 500 online, 50 msg/s on 2 vCPU / 4 GB | 5,000 members joined in 17 s (join p95 43 ms); 50.0 msg/s for 5 minutes, 15,000 posts, 0 errors; post round trip p50 0.5 ms, p95 2.0 ms, p99 3.0 ms; every one of the 7,125,000 deliveries to 475 watchers arrived, and the 25 decrypting watchers saw delivery p95 3.1 ms, p99 5.7 ms; server CPU 0.02 cores average, memory 80 MiB (peak 129 MiB) | Yes |
| Relay | 2,000 concurrent connections | 2,000 connections opened in 6.7 s, connect p50 304 ms: the burst passes `pow_new_per_second`, so the relay asks for proof-of-work, as configured (with `OPENARCANUM_RELAY_POW_MODE=off` the same burst opens in 0.24 s, p50 11 ms); held 2 minutes with none dropped; 12,000 publishes, all 228,000 deliveries arrived, delivery p95 12 ms, p99 20 ms; relay CPU 0.02 cores, memory 133 MiB (about 66 KiB per connection) | Yes |
| Maintainer node | 10,000 listings, search p95 < 200 ms | 10,000 signed listings stored in 3.1 s; 16 searchers back to back for 60 s: 79,417 searches (1,323/s), 0 errors, 0 empty; p50 7 ms, p95 37 ms, p99 55 ms; node CPU 1.85 of 2 cores (saturated by design), memory 51 MiB (peak 95 MiB) | Yes |

### Search index rebuild

The community server keeps its search index in memory and rebuilds it from
the encrypted messages at every start (docs/operations.md "Search
index"). The community load test doesn't restart the server, so this is
measured separately, in-process, by `BenchmarkSearchRebuild`
(`internal/community/search_rebuild_internal_test.go`): a data directory with
N stored messages (12-24 words each, two channels, one in seven deleted) is
rebuilt three times per size. Recorded 2026-10-24 on an AMD Ryzen 9 5950X,
`GOMAXPROCS=6`, local SSD:

| Messages stored (indexed) | Rebuild | Go heap allocated | Go heap peak above start | Resident growth (mostly the index) |
|---|---|---|---|---|
| 10,000 (8,571) | 90 ms | 13 MB | 2.3 MiB | 4.5 MiB |
| 50,000 (42,857) | 480 ms | 66 MB | 2.3 MiB | 13 MiB |
| 200,000 (171,429) | 2.14 s | 264 MB | 2.3 MiB | 50 MiB |

The rebuild streams rows into the index in transactions of 2,000, so its
working memory is flat; time and the index's own memory grow linearly. `TestSearchRebuildStreamsEveryMessage` (part
of `go test ./...`; `OPENARCANUM_SEARCH_REBUILD_N` sets its size, default 6,000)
checks that every undeleted message, across several transactions, is indexed
and no deleted one is.

What these don't show: behaviour over real networks and slower disks, the
community server's cost with files and voice in the mix, and how much
headroom is left (the community server was nearly idle; a higher-rate run is
`LOAD_RATE=...`). The relay allocates per-connection buffers only after a
connection starts, which keeps an idle connection near 66 KiB.
