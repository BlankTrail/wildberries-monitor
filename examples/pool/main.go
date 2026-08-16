// SPDX-License-Identifier: AGPL-3.0-or-later

// Command pool demonstrates the blanktrail SDK end to end: run preflight, open a
// pool of ports over a proxy list, and fetch a URL through leased ports.
//
//	go run ./examples/pool -url https://example.com -proxies proxies.txt
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
)

func main() {
	var (
		control = flag.String("control", "http://127.0.0.1:8891", "BlankTrail control API base URL")
		apiKey  = flag.String("key", os.Getenv("BLANKTRAIL_API_KEY"), "BlankTrail API key")
		target  = flag.String("url", "", "URL to fetch (required)")
		list    = flag.String("proxies", "", "proxy list file (empty = direct egress)")
		threads = flag.Int("threads", 2, "parsing threads")
		perThr  = flag.Int("ports-per-thread", 3, "BlankTrail ports per thread")
		count   = flag.Int("n", 6, "how many requests to send")
	)
	flag.Parse()
	if *target == "" {
		log.Fatal("-url is required")
	}

	ctx := context.Background()
	client, err := blanktrail.NewClient(*control, *apiKey)
	if err != nil {
		log.Fatalf("control client: %v", err)
	}

	rep := blanktrail.Preflight(ctx, client, blanktrail.PreflightInput{
		Domains: []string{hostOf(*target)},
		Ports:   *threads * *perThr,
	})
	for _, f := range rep.Findings {
		fmt.Printf("[%s] %s\n      %s\n      → %s\n", f.Severity, f.Title, f.Detail, f.Action)
	}
	if !rep.OK() {
		log.Fatal("preflight failed; fix the findings above and try again")
	}

	channels := []blanktrail.Channel{blanktrail.NewDirectChannel("direct")}
	if *list != "" {
		rotor, err := blanktrail.NewRotor(ctx, blanktrail.Source{Kind: "file", Location: *list})
		if err != nil {
			log.Fatalf("proxy list: %v", err)
		}
		channels = []blanktrail.Channel{blanktrail.NewListChannel("list", rotor)}
	}

	pool, err := blanktrail.NewPool(ctx, blanktrail.PoolConfig{
		Client:             client,
		Threads:            *threads,
		PortsPerThread:     *perThr,
		Spec:               blanktrail.DefaultPortSpec(),
		Channels:           channels,
		CA:                 rep.CA,
		DelayMin:           3 * time.Second,
		DelayMax:           8 * time.Second,
		RenewAfterRequests: 25,
		RenewAfterInterval: 15 * time.Minute,
	})
	if err != nil {
		log.Fatalf("open pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	fmt.Printf("pool: %d ports, cooldown %v\n", pool.Size(), pool.Cooldown())

	for i := 0; i < *count; i++ {
		if err := fetchOnce(ctx, pool, *target, i); err != nil {
			log.Printf("request %d: %v", i, err)
		}
	}
	fmt.Printf("stats: %+v\n", pool.Stats())
}

func fetchOnce(ctx context.Context, pool *blanktrail.Pool, target string, i int) error {
	lease, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer lease.Release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := lease.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Printf("#%d port=%d egress=%s status=%d bytes=%d\n",
		i, lease.Port(), lease.Egress(), resp.StatusCode, len(body))
	return nil
}

func hostOf(rawURL string) string {
	if u, err := http.NewRequest(http.MethodGet, rawURL, nil); err == nil {
		return u.URL.Hostname()
	}
	return rawURL
}
