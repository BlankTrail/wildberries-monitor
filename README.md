# wildberries-monitor

Open-source Wildberries parser and monitor written in Go: a web panel, a
collection engine, change tracking with rules, Telegram notifications, and
exports to spreadsheets and databases.

**This program only works through [BlankTrail Proxy](https://github.com/BlankTrail).**
Wildberries is behind a JS challenge, so a BlankTrail licence that includes
Challenge Breaker is required. Without it, requests will not go through — this
is a hard requirement, not a degraded mode.

## Install and run

Download the archive for your platform from the releases page, unpack it, and
run `start.bat` (Windows) or `./start.sh` (macOS, Linux). Or build it yourself:

```
go build ./cmd/wbmon
```

The first start prints the address of the panel and a generated password, and
writes the same password to `first-run.txt` in the data directory. The panel is
on `http://127.0.0.1:8760/` by default, and it is closed to the rest of the
network until you set a password of your own — a generated one anybody can read
out of a file is not a password once the port is reachable.

Data lives in the platform's own place rather than beside the binary
(`%LOCALAPPDATA%\BlankTrail\wbmon`, `~/Library/Application Support/BlankTrail/wbmon`,
`~/.local/share/wbmon`), and `WBMON_DATA` or `-data` moves it.

```
wbmon -port 8760          panel port
wbmon -data /some/where   where the database lives
wbmon -open=false         do not open a browser (for a service)
wbmon -lan                serve beyond this machine (needs your own password)
```

Then, in the panel: open **Настройки**, enter the BlankTrail address and API
key and press **Проверить соединение**. Nothing collects anything until that
check passes.

## What it does

* **Задачи** — build a collection job: what to enumerate (a phrase, a seller,
  a brand, a list of article numbers, the paid placements for a phrase), which
  of 38 fields to collect, over which regions and for which audience. The
  screen prices the job in requests before you start it, and says which of its
  numbers is a guess.
* **Правила** — say what is worth being told about: price and stock moves,
  places in the results, sizes and warehouses disappearing, ratings and review
  counts. Conditions combine the change and the state it ended at ("fell more
  than 5% while stock is under ten"). Thresholds, quiet hours, per-product rate
  limits and deduplication are all there, and every match is logged — including
  the ones that were suppressed, with the reason.
* **Результаты** — a table of what was collected, and the same data as a file:
  CSV, XLSX, JSON, JSONL or a SQLite database. Four of the five stream; the
  fifth cannot, because a SQLite file is finished by seeking back to its header.
* **Telegram** — notifications, and a bot that answers `/jobs`, `/run`, `/stop`
  and `/export`. It reaches Telegram directly where that works and through your
  BlankTrail port where it does not, and the settings screen shows which.

## What it does not do

Stated plainly, because the alternative is a checkbox that collects nothing:

* **Promotions.** This build has no source for them, so no promotion fields and
  no promotion rules exist.
* **Comparison against competitors.** Every rule of that kind is phrased
  relative to "my" product, which comes from a seller profile this build does
  not have yet.
* **MTProto.** Telegram is reached over the Bot API. If `api.telegram.org` is
  blocked for you *and* your BlankTrail tariff does not allow it either, there
  is no path yet — that is the next milestone.

## Using the SDK

```go
client, _ := blanktrail.NewClient("http://127.0.0.1:8891", apiKey)

report := blanktrail.Preflight(ctx, client, blanktrail.PreflightInput{
    Domains: []string{"example.com"},
    Ports:   6,
})
if !report.OK() {
    for _, f := range report.Blocking() {
        log.Printf("%s: %s → %s", f.Title, f.Detail, f.Action)
    }
    return
}

pool, _ := blanktrail.NewPool(ctx, blanktrail.PoolConfig{
    Client:         client,
    Threads:        2,
    PortsPerThread: 3,
    Spec:           blanktrail.DefaultPortSpec(),
    Channels:       []blanktrail.Channel{blanktrail.NewDirectChannel("direct")},
    CA:             report.CA,
})
defer pool.Close()

lease, _ := pool.Acquire(ctx)
defer lease.Release()
resp, _ := lease.Do(req)
```

A runnable version is in `examples/pool`.

### How a port behaves

A port is a session: one fingerprint, one cookie jar, one egress IP. Requests on
a port are serialised by the pool, through the lease and the cooldown, rather
than by a limit set on the port itself — the port's own concurrency governs
everything flowing through it, not only the requests we issue, so a low limit can
starve the port's own challenge handling. The pool never hands a port back before
its cooldown has elapsed — by default `ports-per-thread × the midpoint of the
delay range`, which is the same guarantee a rigid per-thread ring gives, with
less idling.

On a failure the leased client reacts on its own. A port that fails several
times in a row gets a fresh egress IP, and one that keeps failing after that is
quarantined. `blanktrail` does not try to tell a challenge from a block from a
rate limit — by default any non-2xx or transport error counts the same — but it
does not have to guess either: `PoolConfig.CountFailure` lets whoever knows the
target say which statuses should count. `wb.CountFailure` is that answer for
Wildberries, and `examples/wbsearch` wires it in; without it a challenge (498)
or a request of ours the edge rejected (403) would rotate the egress, which
cannot fix either and throws away a solved challenge on the way.
Independently of all that, a port's whole identity is renewed after N requests
or after a time interval.

The leased client's own repeating is budgeted twice, because the two failures
call for different remedies. `MaxRetriesPerReq` (default 4) repeats a request
that met a rate limit or a server error, honouring `Retry-After` — the origin
asking to be asked again, where repeating through the same egress is exactly
right. `MaxTransportRetries` (default 1) repeats a request whose connection died,
where it mostly is not: that is evidence about the egress, and a caller who can
replace the egress between attempts does the job far better. One immediate
re-dial is kept because a connection can die between the idle-pool check and the
write, which nothing above this layer can tell apart from a bad proxy.

A lease holder can also ask for a new egress itself, with `Lease.RotateEgress`.
The pool judges an egress by what it can see — connection failures and non-2xx
statuses — and a caller that knows the target may recognise a failure it cannot:
a response that is technically fine and still means this exit address is not
getting through. Releasing the lease and taking a fresh one is not the same
thing, because that is a different port, not a different proxy. The change
clears the port's consecutive-failure count, so the pool's own schedule measures
the new address instead of carrying the old one's history into it.

## Reading Wildberries data (the `wb` package)

`wb` reads Wildberries search results and product cards through a leased
BlankTrail port. Routing, fingerprints and challenge solving are `blanktrail`'s
job, not this package's; what `wb` does decide is what a response means and what
is worth doing about it — how many times a request the edge challenged, or the
proxy killed before it ever answered, is worth repeating; when the port's
proxy has had enough tries and should be replaced; and when the port itself is
unreachable, so the fetch should move to another one and take the rest of its
budget with it
(`wb.RetryPolicy`, and `wb.DefaultRetryPolicy` for the two sensible starting
points). It takes a `Leaser` (`wb.FromPool` adapts a `*blanktrail.Pool`) and
a `*wb.Sessions`, and hands back a `*wb.Client` that every request goes
through.

```go
eps := wb.DefaultEndpoints()

// A pool with proxy channels can search for one that gets through, so a
// challenge is worth retrying further than it is on direct egress; pass
// DefaultRetryPolicy(false), or use NewClient, when there is nothing to search.
client := wb.NewClientWithRetry(wb.FromPool(pool), wb.NewSessions(),
    wb.DefaultRetryPolicy(true))

env, err := client.SearchPage(ctx, eps, wb.SearchQuery{
    Query: "кроссовки женские",
    Dest:  "1259570991",
    Page:  1,
})
for _, p := range env.Products {
    sale, _ := p.SalePrice()
    fmt.Println(p.Rank, p.Name, sale)
}

basket := wb.NewBasket(client)
nm := env.Products[0].ID
card, product, err := client.Card(ctx, basket, eps, nm, "1259570991", wb.AppWeb)
```

A page shorter than the site's own page size is the end of the result set —
there is no other reliable signal, and a caller walking multiple pages should
stop on that, not on arithmetic against the total the payload reports.
`env.Dropped` counts items the page named but that failed extraction; that is
a partial drop, not a failed page, and rank is computed from each product's
position on the page it came from, so a drop never shifts another row's rank.

Prices are `Money`: an integer number of minor units with a currency, never a
float, so a payload's kopecks are never silently rounded away.

A runnable version — preflight, open a pool, walk a search or fetch a card,
write JSONL, print a summary of what happened — is in `examples/wbsearch`:

```bash
go run ./examples/wbsearch -query "кроссовки женские" -dest 1259570991 -pages 3
go run ./examples/wbsearch -card 1309449623 -dest -5892277
```

## Building from source

```
go build ./...     # the library and the binary
go test ./...      # the suite
```

No CGO, on any platform: `modernc.org/sqlite` was chosen so that one machine
can cross-compile every release. There are two direct dependencies in total.

## Licence

AGPL-3.0-or-later. See `LICENSE` and `NOTICE`.

If you run this program for others over a network, section 13 of the AGPL
requires you to offer them the source code. The built-in web UI carries a link
to this repository for exactly that reason — do not remove it.

## Legal note

This tool reads publicly available data. You are responsible for complying with
the Wildberries terms of service and with applicable data-protection law.
Product reviews contain personal data of real people; treat exported data
accordingly.
