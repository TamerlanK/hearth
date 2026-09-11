# Benchmarks

Numbers from one laptop, measured on 2026-09-10 with the commands shown, so
they can be reproduced and argued with. Both the server and the load generator
ran on the same machine over loopback, which means the load generator competes
with the server for the same cores; every number here is therefore a floor for
the server, not a ceiling.

| | |
|---|---|
| CPU | Intel Core i7-13620H, 10 cores / 16 threads (6 P + 4 E), scaling governor at 32% base |
| Memory | 15 GiB |
| OS | Ubuntu 24.04.4, Linux 7.0.0-31-generic |
| Go | go1.27.0 linux/amd64 |
| Build | `make build` (default flags, no PGO) |
| Server flags | `hearth serve --addr 127.0.0.1:4000 --metrics-addr 127.0.0.1:9090 --max-clients 0 --max-per-ip 0 --log-level warn` |

## Micro-benchmarks

```sh
make bench      # go test -run '^$' -bench . -benchmem ./...
```

Median of three runs:

| Benchmark | ns/op | B/op | allocs/op | What it measures |
|---|---:|---:|---:|---|
| `BenchmarkFanout/clients=10` | 395 | 0 | 0 | hub hands one message to 10 outboxes |
| `BenchmarkFanout/clients=100` | 3 457 | 0 | 0 | … to 100 outboxes |
| `BenchmarkFanout/clients=1000` | 35 634 | 0 | 0 | … to 1000 outboxes |
| `BenchmarkJSONEncode` | 509 | 432 | 3 | one `msg` event to an `io.Writer` |
| `BenchmarkJSONDecode` | 449 | 80 | 2 | one `msg` command line into a `Command` |
| `BenchmarkDecodeEvent` | 774 | 144 | 1 | client side: one event line into an `Event` |
| `BenchmarkTextEncode` | 208 | 181 | 6 | one `msg` event rendered for telnet |
| `BenchmarkRing/push` | 4.5 | 0 | 0 | history append |
| `BenchmarkRing/snapshot` | 221 | 896 | 1 | history replay copy, 50 entries |

Fan-out is linear at about **35 ns per recipient** with no allocation: the hub
does one mutex acquire and one buffered channel send per member. The hub is
never the limit in the load runs below; the per-client writers are.

## Load runs

`cmd/hearth-load` connects N clients through `pkg/client`, spreads them across
`--rooms` rooms, has every client send `--rate` messages per second with the
send time embedded in the text, and measures end-to-end latency on every
delivery at every receiver. Server CPU, RSS and drop counters come from the
metrics endpoint before and after the run. Delivered % is deliveries received
over deliveries expected (`sent × room size`).

```sh
make build build-load
bin/hearth serve --addr 127.0.0.1:4000 --metrics-addr 127.0.0.1:9090 --max-clients 0 --max-per-ip 0 --log-level warn &
bin/hearth-load --clients 100  --rooms 1  --rate 1    --duration 30s
bin/hearth-load --clients 1000 --rooms 10 --rate 1    --duration 30s
bin/hearth-load --clients 1000 --rooms 1  --rate 0.2  --duration 30s
bin/hearth-load --clients 5000 --rooms 50 --rate 1    --duration 30s
bin/hearth-load --clients 5000 --rooms 1  --rate 0.02 --duration 30s
```

| clients | rooms | rate/client | msg/s in | deliveries/s | delivered | p50 | p95 | p99 | max | server drops | server CPU | server RSS |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 100 | 1 | 1/s | 100 | 9 996 | 100.0% | 301 µs | 475 µs | 639 µs | 1.3 ms | 0 | 9% | 24 MB |
| 1000 | 10 | 1/s | 1 000 | 99 994 | 100.0% | 298 µs | 558 µs | 775 µs | 3.3 ms | 0 | 91% | 65 MB |
| 1000 | 1 | 0.2/s | 200 | 199 987 | 100.0% | 1.07 ms | 2.57 ms | 4.3 ms | 13.9 ms | 0 | 164% | 65 MB |
| 5000 | 50 | 1/s | 4 998 | 499 801 | 100.0% | 3.29 ms | 23.2 ms | 35.1 ms | 197 ms | 0 | 689% | 246 MB |
| 5000 | 1 | 0.02/s | 100 | 497 441 | 99.7% ¹ | 20.7 ms | 70.3 ms | 126 ms | 234 ms | 0 | 683% | 254 MB |

CPU is percent of one core, so 689% is about seven of the sixteen hardware
threads. RSS works out to **about 45 KB per connection** (three goroutines, a
4 KB scanner buffer and a 32-slot outbox each) over a 20 MB baseline.

These numbers predate D85, which replaced the outbox channel with a mutex
guarded slice so that a client's own replies are never dropped. A same
machine A/B of `BenchmarkFanout` before and after that change (Windows,
go1.27, `-count=3`) put the slice ahead: 1000 recipients went from 54 µs to
38 µs per broadcast, 100 from 4.1 µs to 3.3 µs, still with zero allocations
per op once the slices have grown to their working size. The load runs above
have not been repeated; the fan-out was never the bottleneck they found.

¹ Seventeen of the 5000 clients were disconnected for outbox overload during the
*connect* phase, before the measurement window opened, and their share of the
deliveries never happened. See *Connect storms* below.

### Over the cliff

```sh
bin/hearth-load --clients 1000 --rooms 1 --rate 1 --duration 30s
```

1000 clients in one room at one message per second each asks for one million
deliveries per second. The server got to **930 000/s at 647% CPU** and then the
design did what it says on the tin: 3% of events were dropped at full outboxes
(474 089 in 17 s), 27 clients were disconnected for 100 consecutive drops, and
the rest were disconnected by the 5 s write deadline because the load generator
on the same box could no longer drain a million JSON lines a second. p99 was
still 25.9 ms for the messages that did arrive. Nobody blocked the hub, nobody
blocked anybody else, and the server was idle again with 13 goroutines the
moment the clients were gone.

### Where the time goes

CPU profile of the server for 15 s of the 5000-client, 50-room run, taken from
the pprof endpoint on the metrics port:

```sh
go tool pprof -top -nodecount=10 'http://127.0.0.1:9090/debug/pprof/profile?seconds=15'
```

```
Duration: 15s, Total samples = 116.37s (775.66%)
      flat  flat%   sum%        cum   cum%
    64.50s 55.43% 55.43%     64.50s 55.43%  internal/runtime/syscall/linux.Syscall6
     2.89s  2.48% 57.91%     12.47s 10.72%  encoding/json/v2.makeStructArshaler.func2
     2.28s  1.96% 59.87%      2.28s  1.96%  runtime.(*lfstack).pop
     1.60s  1.37% 61.24%      3.64s  3.13%  runtime.chanrecv
     1.53s  1.31% 62.56%      1.53s  1.31%  runtime.memmove
     1.40s  1.20% 63.76%      1.58s  1.36%  encoding/json/internal/jsonwire.AppendQuote
     1.35s  1.16% 64.92%    102.29s 87.90%  internal/server.(*client).writeLoop
     1.30s  1.12% 66.04%      2.65s  2.28%  internal/poll.runtime_pollSetDeadline
     1.23s  1.06% 68.16%     97.25s 83.57%  internal/server.(*client).writeEvent
     0.86s  0.74% 72.99%      4.77s  4.10%  internal/server.(*client).trySend
```

Reading it bottom-up:

- **88% of all CPU is inside `writeLoop`**, the per-client goroutine that
  encodes and writes events. The hub's entire contribution (`trySend`) is 4%.
- Of that, **55% is the `write(2)` syscall**. Every event to every recipient is
  its own syscall: 500 000 deliveries per second is 500 000 syscalls per second.
- **11% is JSON encoding.** The same event is marshalled once *per recipient*;
  in the 5000-client run every message is encoded 100 times.
- **2.3% is `SetWriteDeadline`**, also once per event.
- The runtime scheduler (`findRunnable`, `lfstack`, `chanrecv`) is a few
  percent: 15 000 goroutines handing single events around.

So the bottleneck is not the hub, the lock, or the channel; it is the
**one-syscall-per-event write path**. Latency at 5000 clients (p50 3.3 ms, p99
35 ms) is queueing in the outboxes while 5000 writer goroutines take turns on
seven cores.

### What I would change, and why I have not

1. **Batch writes.** Give each `writeLoop` a `bufio.Writer` and flush only when
   the outbox is empty (or the buffer is full). Under load a writer would wake
   up, find several events queued, and write them with one syscall. This alone
   should cut the 55% syscall share by whatever the average batch depth is; at
   the 5000-client point the outboxes are visibly queueing, so batches would be
   deep. Cost: one more deadline-handling path and a flush rule to test. Not done
   because at the loads this server is actually for, one message to a room of a
   hundred people is 100 syscalls and nobody notices; the change is worth doing
   only once the profile says so, which it now does.
2. **Encode once per broadcast.** Marshal the event to bytes in the hub and hand
   each recipient the same `[]byte` instead of the `Event`. Removes the 11%
   JSON share and its allocations. Cost: `seq` is per connection and is stamped
   at write time, so it would have to move out of the encoded body or be
   patched in per client. Not done because the sequence number is a documented
   part of the protocol and changing where it lives is a protocol decision,
   not a performance tweak.
3. **Shard the hub.** Not needed. The fan-out benchmark says the single hub
   goroutine spends 35 µs delivering one message to 1000 outboxes, so it could
   do 28 000 such broadcasts a second before it became the limit, and the load
   runs never got it above 4% of CPU. Sharding by room would buy nothing until
   the write path above is fixed, and it would cost the atomic name-uniqueness
   check that the single hub gives for free.
4. **Cap the connect storm.** See below.

### Connect storms

Every `join` (including the implicit one at connect) is broadcast to the whole
room, so connecting N clients into one room emits N²/2 events. At 200 connects
per second into a 5000-member room that is a million events per second before
anyone has said anything, and it is why the load tool paces connections
(`--connect-rate`, default 200/s) and why 17 clients in the single-room 5000
run were disconnected before the measurement started. The name-uniqueness
check was also a linear walk over every member and showed at 8% of CPU in a
profile taken during the connect phase; it is now a map (see *Name lookup*),
and what remains of the storm is the join notices themselves.

A server meant for big rooms would stop announcing joins above some room size,
or coalesce them. That is not done because the `--max-clients` default is 100
and the room shape this is built for is tens of people, not thousands.

### Name lookup

Registration, `/nick` and `/msg` each look a name up in the hub. Until D72 that
was a walk over every member of every room, so the cost of a private message
grew with the number of people online. It is now a `map[string]*client` that
the hub writes in the same steps that change membership.

| online | before | after |
|-------:|-------:|------:|
| 100 | 565 ns | 155 ns |
| 1000 | 4.3 µs | 153 ns |
| 5000 | 23.8 µs | 157 ns |

`BenchmarkPrivateMessage`: one `/msg` through the hub, 144 B and 1 alloc per
op in both columns (the event itself). `BenchmarkConnectStorm/clients=5000`,
five thousand registrations into one empty hub, went from 378 ms to 282 ms on
the same machine; the 282 ms is the 12.5 million join notices.

### What the load tool found

Writing the tool found a real bug in `pkg/client`: cancelling the context of a
`Send` set a deadline on the *whole* socket, so the reader goroutine failed on
its next read and the client tore down a perfectly good connection. The load
tool hit it once per run when a sender's context expired mid-call. Fixed with
a write-only deadline that is cleared after the interrupt, with
`TestCancelledSendKeepsConnectionUsable` as the regression test.
