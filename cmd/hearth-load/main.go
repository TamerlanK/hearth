package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
)

const (
	dialParallelism     = 64
	maxSamplesPerClient = 1_000_000
	settle              = 2 * time.Second
)

type options struct {
	addr        string
	clients     int
	rooms       int
	rate        float64
	duration    time.Duration
	connectRate float64
	metrics     string
}

type participant struct {
	c        *client.Client
	sent     atomic.Int64
	received atomic.Int64
	lost     atomic.Bool

	mu      sync.Mutex
	samples []int64
}

type serverStats struct {
	cpuSeconds float64
	rssBytes   float64
	dropped    float64
	goroutines float64
	ok         bool
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "hearth-load:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	var o options
	fs := flag.NewFlagSet("hearth-load", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&o.addr, "addr", "127.0.0.1:4000", "server to load")
	fs.IntVar(&o.clients, "clients", 100, "connections to open")
	fs.IntVar(&o.rooms, "rooms", 1, "rooms to spread the clients across, round robin")
	fs.Float64Var(&o.rate, "rate", 1, "messages per second sent by each client")
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "how long to send for")
	fs.Float64Var(&o.connectRate, "connect-rate", 200, "connections opened per second; every join fans out to the whole room, so a storm into one big room overflows outboxes")
	fs.StringVar(&o.metrics, "metrics", "http://127.0.0.1:9090/metrics", "server metrics URL for CPU, memory and drops; empty skips it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.clients < 1 || o.rooms < 1 || o.rate <= 0 || o.duration <= 0 || o.connectRate <= 0 {
		return errors.New("clients and rooms must be at least 1; rate, connect-rate and duration positive")
	}
	if o.rooms > o.clients {
		o.rooms = o.clients
	}

	log := slog.New(slog.NewTextHandler(errOut, nil))
	ps, err := connectAll(ctx, o, log)
	if err != nil {
		return err
	}
	defer closeAll(ps)

	before := scrape(ctx, o.metrics)
	start := time.Now()
	sendCtx, stop := context.WithTimeout(ctx, o.duration)
	defer stop()
	var senders sync.WaitGroup
	for _, p := range ps {
		senders.Add(1)
		go func() {
			defer senders.Done()
			p.sendLoop(sendCtx, o.rate)
		}()
	}
	senders.Wait()
	elapsed := time.Since(start)
	time.Sleep(settle)
	after := scrape(ctx, o.metrics)

	report(out, o, ps, elapsed, before, after)
	return nil
}

func connectAll(ctx context.Context, o options, log *slog.Logger) ([]*participant, error) {
	start := time.Now()
	ps := make([]*participant, o.clients)
	errs := make([]error, o.clients)
	sem := make(chan struct{}, dialParallelism)
	pace := time.NewTicker(time.Duration(float64(time.Second) / o.connectRate))
	defer pace.Stop()
	var wg sync.WaitGroup
	for i := range ps {
		wg.Add(1)
		sem <- struct{}{}
		<-pace.C
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			room := ""
			if o.rooms > 1 {
				room = "load-" + strconv.Itoa(i%o.rooms)
			}
			dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			c, err := client.Dial(dialCtx, o.addr, client.Options{Name: "load-" + strconv.Itoa(i), Room: room, KeepAlive: -1})
			if err != nil {
				errs[i] = err
				return
			}
			p := &participant{c: c}
			ps[i] = p
			go p.receiveLoop()
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		closeAll(ps)
		return nil, fmt.Errorf("connect %d clients: %w", o.clients, err)
	}
	log.Info("connected", "clients", o.clients, "rooms", o.rooms, "took", time.Since(start).Round(time.Millisecond))
	return ps, nil
}

func closeAll(ps []*participant) {
	var wg sync.WaitGroup
	for _, p := range ps {
		if p == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.c.Close(); err != nil {
				slog.Warn("close client", "err", err)
			}
		}()
	}
	wg.Wait()
}

func (p *participant) sendLoop(ctx context.Context, rate float64) {
	interval := time.Duration(float64(time.Second) / rate)
	select {
	case <-time.After(rand.N(interval)):
	case <-ctx.Done():
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		text := strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := p.c.Say(ctx, text); err != nil {
			return
		}
		p.sent.Add(1)
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (p *participant) receiveLoop() {
	for e := range p.c.Events() {
		if e.Kind != protocol.Msg {
			continue
		}
		sentAt, err := strconv.ParseInt(e.Text, 10, 64)
		if err != nil {
			continue
		}
		p.received.Add(1)
		p.mu.Lock()
		if len(p.samples) < maxSamplesPerClient {
			p.samples = append(p.samples, time.Now().UnixNano()-sentAt)
		}
		p.mu.Unlock()
	}
	p.lost.Store(true)
}

func scrape(ctx context.Context, url string) serverStats {
	var s serverStats
	if url == "" {
		return s
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return s
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("scrape metrics", "url", url, "err", err)
		return s
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("close metrics body", "err", err)
		}
	}()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		switch name {
		case "process_cpu_seconds_total":
			s.cpuSeconds = v
		case "process_resident_memory_bytes":
			s.rssBytes = v
		case "hearth_dropped_messages_total":
			s.dropped = v
		case "go_goroutines":
			s.goroutines = v
		}
	}
	s.ok = sc.Err() == nil
	return s
}

func report(out io.Writer, o options, ps []*participant, elapsed time.Duration, before, after serverStats) {
	var sent, received, clientDrops, lost int64
	var samples []int64
	for _, p := range ps {
		sent += p.sent.Load()
		received += p.received.Load()
		clientDrops += int64(p.c.Stats().Dropped)
		if p.lost.Load() {
			lost++
		}
		p.mu.Lock()
		samples = append(samples, p.samples...)
		p.mu.Unlock()
	}
	expected := int64(0)
	for i, p := range ps {
		expected += p.sent.Load() * int64(roomSize(o, i))
	}
	slices.Sort(samples)
	secs := elapsed.Seconds()

	fmt.Fprintf(out, "| clients | rooms | rate/client | duration | sent | delivered | delivered %% | msg/s in | msg/s out | p50 | p95 | p99 | max | server drops | client drops | disconnected | server CPU | server RSS | goroutines |\n")
	fmt.Fprintf(out, "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	fmt.Fprintf(out, "| %d | %d | %.2g/s | %s | %d | %d | %.1f%% | %.0f | %.0f | %s | %s | %s | %s | %s | %d | %d | %s | %s | %s |\n",
		o.clients, o.rooms, o.rate, elapsed.Round(time.Millisecond),
		sent, received, pct(received, expected), float64(sent)/secs, float64(received)/secs,
		percentile(samples, 0.50), percentile(samples, 0.95), percentile(samples, 0.99), percentile(samples, 1),
		serverDelta(before, after, func(s serverStats) float64 { return s.dropped }),
		clientDrops, lost,
		cpuPercent(before, after, elapsed+settle), rss(after), goroutines(after))
}

func roomSize(o options, i int) int {
	if o.rooms <= 1 {
		return o.clients
	}
	size := o.clients / o.rooms
	if i%o.rooms < o.clients%o.rooms {
		size++
	}
	return size
}

func pct(part, whole int64) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

func percentile(sorted []int64, q float64) string {
	if len(sorted) == 0 {
		return "n/a"
	}
	i := int(q*float64(len(sorted))) - 1
	i = max(0, min(i, len(sorted)-1))
	return time.Duration(sorted[i]).Round(time.Microsecond).String()
}

func serverDelta(before, after serverStats, field func(serverStats) float64) string {
	if !before.ok || !after.ok {
		return "n/a"
	}
	return strconv.FormatFloat(field(after)-field(before), 'f', 0, 64)
}

func cpuPercent(before, after serverStats, wall time.Duration) string {
	if !before.ok || !after.ok {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*(after.cpuSeconds-before.cpuSeconds)/wall.Seconds())
}

func rss(s serverStats) string {
	if !s.ok {
		return "n/a"
	}
	return fmt.Sprintf("%.0f MB", s.rssBytes/(1<<20))
}

func goroutines(s serverStats) string {
	if !s.ok {
		return "n/a"
	}
	return strconv.FormatFloat(s.goroutines, 'f', 0, 64)
}
