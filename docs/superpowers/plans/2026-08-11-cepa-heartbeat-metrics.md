# CEPA Registration/Heartbeat Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose per-publisher CEPA liveness on `/metrics` so a dashboard can tell an idle NAS from a dead publishing path.

**Architecture:** `pkg/metrics` gains a mutex-guarded map of publisher hosts, capped at 64 entries, alongside its existing atomics. `pkg/server` stamps the peer on every PUT before branching. `pkg/prometheus` adds a `prometheus.Collector` — `CounterFunc`/`GaugeFunc` cannot carry a dynamic label set — that emits one const metric per peer from a single snapshot per scrape.

**Tech Stack:** Go 1.26.5, stdlib only for state and tests, `prometheus/client_golang` confined to `pkg/prometheus`.

**Spec:** `docs/superpowers/specs/2026-08-10-cepa-heartbeat-metrics-design.md`
**Issue:** [#27](https://github.com/fjacquet/cee-exporter/issues/27)

## Global Constraints

- **Go 1.26.5** (`go.mod`). `make ci` = lint + test + build + vuln, and is the gate.
- **stdlib-only tests.** No testify. `go.mod` has no test dependencies; do not add one.
- **Table-driven with `t.Run`** for all multi-case tests.
- **White-box tests** — test files declare the same package as the code under test (`package metrics`, `package server`, `package ceeprometheus`).
- **`errorlint` runs with `comparison: true`** — never compare errors with `==`/`!=`, including in tests. Use `errors.Is`.
- **`errcheck` is excluded for `_test.go` but every other linter applies to test files.** Do not write `//nolint:errcheck` in a test file; use `defer func() { _ = f.Close() }()`.
- **`nilerr`** — returning `nil` on a path where `err != nil` is a build failure.
- **Formatters are separate from linters.** `make lint` reports formatting; `make format` (`golangci-lint fmt`) rewrites it.
- **No `time.Sleep` for synchronisation** in tests — use channel signals.
- **Reset `metrics.M` global state** before any test that asserts on it, and restore it in `t.Cleanup` where the existing tests do (see `resetEventsReceived` in `pkg/server/server_test.go:150`).
- **The 3-second CEPA ACK budget is load-bearing.** Nothing added to `ServeHTTP` may block. The added hot-path work is one `RLock` and two atomic stores.
- **A peer is a CEE server, not a NAS Data Mover.** Never write documentation claiming these metrics detect a Data Mover going silent — see the spec's "What a peer is" section. This is the single most important constraint in this plan; the repository's documentation discipline exists to catch exactly this overclaim.
- **`docs-lint.yml` fails CI** if `PROMISES.md`/`requirements.md` cite a test that is not a real function, or use a Status outside the defined vocabulary. Cited test names must match what is actually committed.

---

### Task 1: Per-peer state in `pkg/metrics`

**Files:**
- Modify: `pkg/metrics/metrics.go`
- Test: `pkg/metrics/metrics_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `const MaxPeers = 64`
  - `type PeerStat struct { LastRequestUnix int64; Registrations int64 }`
  - `func (s *Store) RecordPeerRequestAt(host string, t time.Time)`
  - `func (s *Store) RecordPeerRegistration(host string)`
  - `func (s *Store) PeerSnapshot() map[string]PeerStat`
  - `func (s *Store) PeersDropped() int64`
  - `func (s *Store) ResetPeers()`

**Note on the zero value:** `pkg/metrics/metrics_test.go:9` constructs `&Store{}` directly, so a zero-value `Store` must work. The `peers` map is therefore lazily initialised — reading a nil map is legal in Go, writing to one panics.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/metrics/metrics_test.go`:

```go
func TestStore_RecordPeerRequestAt(t *testing.T) {
	s := &Store{}

	// No peers recorded yet.
	if got := s.PeerSnapshot(); len(got) != 0 {
		t.Errorf("initial PeerSnapshot has %d entries, want 0", len(got))
	}

	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))

	snap := s.PeerSnapshot()
	if len(snap) != 1 {
		t.Fatalf("PeerSnapshot has %d entries, want 1", len(snap))
	}
	if got := snap["10.0.2.250"].LastRequestUnix; got != 1_700_000_000 {
		t.Errorf("LastRequestUnix = %d, want 1700000000", got)
	}

	// A second request from the same peer updates the stamp in place rather
	// than adding an entry.
	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_010, 0))

	snap = s.PeerSnapshot()
	if len(snap) != 1 {
		t.Fatalf("PeerSnapshot has %d entries after re-stamp, want 1", len(snap))
	}
	if got := snap["10.0.2.250"].LastRequestUnix; got != 1_700_000_010 {
		t.Errorf("LastRequestUnix after re-stamp = %d, want 1700000010", got)
	}
}

func TestStore_RecordPeerRegistration(t *testing.T) {
	s := &Store{}
	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))

	s.RecordPeerRegistration("10.0.2.250")
	s.RecordPeerRegistration("10.0.2.250")

	if got := s.PeerSnapshot()["10.0.2.250"].Registrations; got != 2 {
		t.Errorf("Registrations = %d, want 2", got)
	}
}

// TestStore_RecordPeerRegistrationUnknownPeer confirms a registration for a
// peer that was never stamped — or was rejected at the cap — neither creates
// an entry nor panics. Creating one here would be a second, uncapped path
// into the map.
func TestStore_RecordPeerRegistrationUnknownPeer(t *testing.T) {
	s := &Store{}

	s.RecordPeerRegistration("192.0.2.99")

	if got := s.PeerSnapshot(); len(got) != 0 {
		t.Errorf("PeerSnapshot has %d entries, want 0 — registration must not create a peer", len(got))
	}
}

// TestStore_PeerCap is the cardinality guard. The remote label is bounded
// only by MaxPeers; without this the registry grows with every distinct
// source IP that ever sends a PUT.
func TestStore_PeerCap(t *testing.T) {
	s := &Store{}
	at := time.Unix(1_700_000_000, 0)

	for i := 0; i < MaxPeers; i++ {
		s.RecordPeerRequestAt(fmt.Sprintf("10.0.0.%d", i), at)
	}

	if got := len(s.PeerSnapshot()); got != MaxPeers {
		t.Fatalf("PeerSnapshot has %d entries, want %d", got, MaxPeers)
	}
	if got := s.PeersDropped(); got != 0 {
		t.Errorf("PeersDropped = %d before exceeding the cap, want 0", got)
	}

	// One past the cap.
	s.RecordPeerRequestAt("10.9.9.9", at)

	if got := len(s.PeerSnapshot()); got != MaxPeers {
		t.Errorf("PeerSnapshot has %d entries after exceeding the cap, want %d", got, MaxPeers)
	}
	if _, ok := s.PeerSnapshot()["10.9.9.9"]; ok {
		t.Error("peer past the cap was recorded; the cap does not hold")
	}
	if got := s.PeersDropped(); got != 1 {
		t.Errorf("PeersDropped = %d, want 1 — exceeding the cap must be visible, not silent", got)
	}

	// A peer already in the map is still served after the cap is reached.
	s.RecordPeerRequestAt("10.0.0.0", time.Unix(1_700_000_030, 0))
	if got := s.PeerSnapshot()["10.0.0.0"].LastRequestUnix; got != 1_700_000_030 {
		t.Errorf("existing peer LastRequestUnix = %d after cap reached, want 1700000030", got)
	}
}

func TestStore_ResetPeers(t *testing.T) {
	s := &Store{}
	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))
	for i := 0; i <= MaxPeers; i++ {
		s.RecordPeerRequestAt(fmt.Sprintf("10.1.0.%d", i), time.Unix(1_700_000_000, 0))
	}

	s.ResetPeers()

	if got := len(s.PeerSnapshot()); got != 0 {
		t.Errorf("PeerSnapshot has %d entries after ResetPeers, want 0", got)
	}
	if got := s.PeersDropped(); got != 0 {
		t.Errorf("PeersDropped = %d after ResetPeers, want 0", got)
	}
}

// TestStore_PeerConcurrency is the -race guard. CEE's NumberOfThreads
// defaults to 20, so concurrent requests from one host are the normal case,
// not an edge case.
func TestStore_PeerConcurrency(t *testing.T) {
	s := &Store{}
	at := time.Unix(1_700_000_000, 0)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			s.RecordPeerRequestAt(fmt.Sprintf("10.0.1.%d", i%4), at)
			s.RecordPeerRegistration(fmt.Sprintf("10.0.1.%d", i%4))
		}(i)
		go func() {
			defer wg.Done()
			_ = s.PeerSnapshot()
		}()
	}
	wg.Wait()

	if got := len(s.PeerSnapshot()); got != 4 {
		t.Errorf("PeerSnapshot has %d entries, want 4", got)
	}
}
```

Add `"fmt"` and `"sync"` to the import block of `pkg/metrics/metrics_test.go`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/metrics/ -run 'TestStore_(RecordPeer|PeerCap|ResetPeers|PeerConcurrency)' -v`
Expected: FAIL — compile error, `undefined: MaxPeers`, `s.RecordPeerRequestAt undefined`, etc.

- [ ] **Step 3: Write the implementation**

Add to the import block of `pkg/metrics/metrics.go`: `"sync"`.

Add these fields to `type Store struct` (below `lastFsyncAt`):

```go
	// peers tracks CEPA publishers — CEE servers, not NAS Data Movers — by
	// host. Guarded by peersMu rather than being an atomic, because the map
	// itself is mutated on first sight of a peer. Lazily initialised: a
	// zero-value Store must be usable.
	peersMu sync.RWMutex
	peers   map[string]*peerStat

	// peersDropped counts peers rejected because MaxPeers was reached, so
	// that hitting the cap is visible rather than silent.
	peersDropped atomic.Int64
```

Append to `pkg/metrics/metrics.go`:

```go
// MaxPeers bounds the cardinality of the remote label on the CEPA peer
// metrics. Publishers are CEE servers — a handful in any real deployment —
// so this is far above the real ceiling and well below anything that would
// strain the registry. Without it, a port scanner or misconfigured client
// would grow the map without limit.
const MaxPeers = 64

// peerStat is the mutable per-publisher state. The map entry is a pointer so
// that stamping an existing peer needs only a read lock on the map.
type peerStat struct {
	lastRequestUnix atomic.Int64
	registrations   atomic.Int64
}

// PeerStat is an immutable point-in-time copy of one publisher's activity.
type PeerStat struct {
	LastRequestUnix int64
	Registrations   int64
}

// RecordPeerRequestAt stamps the time of the most recent CEPA request from
// host. Called on every PUT — handshake, event batch, or failure — because
// the question it answers is whether the publisher is still talking to us.
//
// If host is not already known and the store is at MaxPeers, the peer is not
// recorded and peersDropped is incremented instead.
func (s *Store) RecordPeerRequestAt(host string, t time.Time) {
	unix := t.Unix()

	s.peersMu.RLock()
	p := s.peers[host]
	s.peersMu.RUnlock()
	if p != nil {
		p.lastRequestUnix.Store(unix)
		return
	}

	s.peersMu.Lock()
	defer s.peersMu.Unlock()

	// Re-check under the write lock: another goroutine may have created this
	// peer between the RUnlock above and this Lock.
	if p := s.peers[host]; p != nil {
		p.lastRequestUnix.Store(unix)
		return
	}
	if len(s.peers) >= MaxPeers {
		s.peersDropped.Add(1)
		return
	}
	if s.peers == nil {
		s.peers = make(map[string]*peerStat, MaxPeers)
	}
	fresh := &peerStat{}
	fresh.lastRequestUnix.Store(unix)
	s.peers[host] = fresh
}

// RecordPeerRegistration counts a CEPA RegisterRequest handshake from host.
//
// It never creates a peer: a peer is created only by RecordPeerRequestAt,
// which enforces MaxPeers. A registration for an unknown host — or for one
// rejected at the cap — is a no-op.
func (s *Store) RecordPeerRegistration(host string) {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	if p := s.peers[host]; p != nil {
		p.registrations.Add(1)
	}
}

// PeerSnapshot returns an immutable copy of the peer table. One call per
// scrape keeps the two per-peer series mutually consistent.
func (s *Store) PeerSnapshot() map[string]PeerStat {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	if len(s.peers) == 0 {
		return nil
	}
	out := make(map[string]PeerStat, len(s.peers))
	for host, p := range s.peers {
		out[host] = PeerStat{
			LastRequestUnix: p.lastRequestUnix.Load(),
			Registrations:   p.registrations.Load(),
		}
	}
	return out
}

// PeersDropped returns the number of peers rejected because MaxPeers was
// reached.
func (s *Store) PeersDropped() int64 {
	return s.peersDropped.Load()
}

// ResetPeers clears the peer table and the drop counter. Provided for test
// isolation, matching the singleton-reset convention the other tests use.
func (s *Store) ResetPeers() {
	s.peersMu.Lock()
	defer s.peersMu.Unlock()
	s.peers = nil
	s.peersDropped.Store(0)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/metrics/ -race -v`
Expected: PASS, all cases, no race reports.

- [ ] **Step 5: Verify the cap test actually guards, by mutation**

This project's rule is that a guard is proven by breaking the behaviour and
watching the test fail — not by reading it. In `RecordPeerRequestAt`, comment
out the `if len(s.peers) >= MaxPeers { ... }` block and run:

Run: `go test ./pkg/metrics/ -run TestStore_PeerCap -v`
Expected: FAIL — `PeerSnapshot has 65 entries after exceeding the cap, want 64`.

Restore the block and re-run; expected PASS. Do not commit the mutation.

- [ ] **Step 6: Commit**

```bash
make format
git add pkg/metrics/metrics.go pkg/metrics/metrics_test.go
git commit -m "feat(metrics): track CEPA publishers by host, capped at MaxPeers

A mutex-guarded map alongside the existing atomics, holding last-request
time and registration count per CEE server. Capped at 64 with a visible
drop counter, because the remote label is otherwise bounded only by the
number of source IPs that ever send a PUT.

Refs #27"
```

---

### Task 2: Stamp the peer in `pkg/server`

**Files:**
- Modify: `pkg/server/server.go` (imports; `ServeHTTP` after the method check; the `IsRegisterRequest` branch)
- Test: `pkg/server/server_test.go`

**Interfaces:**
- Consumes: `metrics.M.RecordPeerRequestAt(host string, t time.Time)`, `metrics.M.RecordPeerRegistration(host string)`, `metrics.M.PeerSnapshot() map[string]metrics.PeerStat`, `metrics.M.ResetPeers()` from Task 1.
- Produces: `func peerHost(addr string) string` (unexported, `package server`).

**Note:** `httptest.NewRequest` sets `RemoteAddr` to `192.0.2.1:1234` by default, so the port-stripping assertions work without extra setup where the default is acceptable; tests that need a specific peer set `req.RemoteAddr` explicitly.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/server/server_test.go`:

```go
// resetPeers isolates the metrics.M peer table for a test and restores an
// empty table afterwards, matching resetEventsReceived's shape.
func resetPeers(t *testing.T) {
	t.Helper()
	metrics.M.ResetPeers()
	t.Cleanup(metrics.M.ResetPeers)
}

func TestPeerHost(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{"ipv4 with port", "10.0.2.250:54321", "10.0.2.250"},
		{"ipv6 with port", "[2001:db8::1]:12228", "2001:db8::1"},
		{"no port", "10.0.2.250", "10.0.2.250"},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerHost(tc.addr); got != tc.want {
				t.Errorf("peerHost(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// TestServeHTTP_StampsPeerOnEveryPath is the liveness guard. The metric must
// answer "is this publisher still talking to us", so every path that
// represents a publisher reaching us — including the failures — stamps it.
// A publisher whose payloads no longer parse is broken but alive, and must
// not read as dead.
func TestServeHTTP_StampsPeerOnEveryPath(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"register request", `<RegisterRequest/>`},
		{"event batch", singleEventXML},
		{"parse error", "<not-well-formed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPeers(t)
			h := newTestHandler(t, &stubWriter{}, 10, 1)

			req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body))
			req.RemoteAddr = "10.0.2.250:54321"
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			snap := metrics.M.PeerSnapshot()
			if _, ok := snap["10.0.2.250"]; !ok {
				t.Fatalf("peer 10.0.2.250 not stamped on the %q path; snapshot = %v", tc.name, snap)
			}
			if got := snap["10.0.2.250"].LastRequestUnix; got == 0 {
				t.Errorf("LastRequestUnix = 0 on the %q path, want a real timestamp", tc.name)
			}
		})
	}
}

// TestServeHTTP_StampsPeerWithoutPort confirms the ephemeral port is stripped.
// CEE's NumberOfThreads defaults to 20, so leaving the port on would create
// up to 20 label values per publisher and grow without bound over time.
func TestServeHTTP_StampsPeerWithoutPort(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	for _, port := range []string{":54321", ":54322", ":54323"} {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`<RegisterRequest/>`))
		req.RemoteAddr = "10.0.2.250" + port
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	snap := metrics.M.PeerSnapshot()
	if len(snap) != 1 {
		t.Fatalf("PeerSnapshot has %d entries for one host on three ports, want 1: %v", len(snap), snap)
	}
	if _, ok := snap["10.0.2.250"]; !ok {
		t.Errorf("peer keyed as %v, want the bare host 10.0.2.250", snap)
	}
}

// TestServeHTTP_CountsRegistrationsOnly confirms registrations count the
// handshake and nothing else — an event batch is not a registration.
func TestServeHTTP_CountsRegistrationsOnly(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	send := func(body string) {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		req.RemoteAddr = "10.0.2.250:54321"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	send(`<RegisterRequest/>`)
	send(`<RegisterRequest/>`)
	send(singleEventXML)

	if got := metrics.M.PeerSnapshot()["10.0.2.250"].Registrations; got != 2 {
		t.Errorf("Registrations = %d, want 2 (two handshakes; the event batch is not one)", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/server/ -run 'TestPeerHost|TestServeHTTP_(StampsPeer|CountsRegistrations)' -v`
Expected: FAIL — compile error, `undefined: peerHost`.

- [ ] **Step 3: Write the implementation**

Add `"net"` to the import block of `pkg/server/server.go`.

In `ServeHTTP`, immediately after the method check (`pkg/server/server.go:46-49`) and **before** `readBody`, insert:

```go
	// Stamp the publisher before anything can fail. A publisher whose body
	// is unreadable or unparseable is broken but alive; recording it only on
	// the success paths would report it as dead. peer is a CEE server, not a
	// NAS Data Mover — see docs/superpowers/specs/2026-08-10-cepa-heartbeat-metrics-design.md.
	peer := peerHost(r.RemoteAddr)
	metrics.M.RecordPeerRequestAt(peer, start)
```

In the `parser.IsRegisterRequest(body)` branch, add the registration count immediately before the existing `slog.Info("cepa_register_request", ...)` call:

```go
		metrics.M.RecordPeerRegistration(peer)
```

Append to `pkg/server/server.go`:

```go
// peerHost reduces a RemoteAddr to its host, dropping the ephemeral port.
// The port changes per connection, so leaving it on would make the remote
// label unbounded. An address that does not parse is used as-is; the
// MaxPeers cap in pkg/metrics still bounds it.
func peerHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/server/ -race -v`
Expected: PASS. `TestServeHTTP_LargeBatchACKsWellUnder3s` must still pass well inside its 2s bound — if it has regressed, the added work is not on the hot path as intended and this task is wrong.

- [ ] **Step 5: Verify the every-path stamp actually guards, by mutation**

Move the two added lines from before `readBody` to inside the event-payload
section (after the `parseErr` check), then run:

Run: `go test ./pkg/server/ -run TestServeHTTP_StampsPeerOnEveryPath -v`
Expected: FAIL on the `register request` and `parse error` subtests — `peer 10.0.2.250 not stamped on the "parse error" path`.

Restore the original placement and re-run; expected PASS. Do not commit the mutation.

- [ ] **Step 6: Commit**

```bash
make format
git add pkg/server/server.go pkg/server/server_test.go
git commit -m "feat(server): record the CEPA publisher on every request path

Stamped before readBody so a publisher whose payload is unreadable still
reads alive rather than dead. The ephemeral port is stripped: CEE opens up
to NumberOfThreads (default 20) connections per host, so keeping it would
make the label unbounded.

Refs #27"
```

---

### Task 3: Expose the peer series in `pkg/prometheus`

**Files:**
- Modify: `pkg/prometheus/handler.go`
- Test: `pkg/prometheus/handler_test.go`

**Interfaces:**
- Consumes: `metrics.M.PeerSnapshot()`, `metrics.M.PeersDropped()`, `metrics.M.RecordPeerRequestAt`, `metrics.M.RecordPeerRegistration`, `metrics.M.ResetPeers()` from Task 1.
- Produces: `type cepaCollector struct{}` with `Describe`/`Collect`, registered by `newRegistry()`. No exported surface — `NewMetricsHandler` and `NewMetricsHandlerWithBuildInfo` keep their existing signatures.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/prometheus/handler_test.go`:

```go
// TestMetricsHandler_CEPAPeerMetrics verifies the per-publisher series are
// scrapeable with their remote label, for more than one publisher — a single
// aggregate would stay green while one of three publishers went dark, which
// is the case these metrics exist to catch.
func TestMetricsHandler_CEPAPeerMetrics(t *testing.T) {
	metrics.M.ResetPeers()
	t.Cleanup(metrics.M.ResetPeers)

	metrics.M.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))
	metrics.M.RecordPeerRegistration("10.0.2.250")
	metrics.M.RecordPeerRegistration("10.0.2.250")
	metrics.M.RecordPeerRequestAt("10.0.2.251", time.Unix(1_700_000_030, 0))

	h := NewMetricsHandler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	required := []string{
		`cee_cepa_last_request_unix_seconds{remote="10.0.2.250"} 1.7e+09`,
		`cee_cepa_registrations_total{remote="10.0.2.250"} 2`,
		`cee_cepa_last_request_unix_seconds{remote="10.0.2.251"} 1.70000003e+09`,
		`cee_cepa_registrations_total{remote="10.0.2.251"} 0`,
		"cee_cepa_peers_dropped_total 0",
	}
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in scrape output, not found\nBody:\n%s", want, body)
		}
	}
}

// TestMetricsHandler_CEPAPeerMetricsEmpty confirms no peers means no series —
// in particular no remote="" artefact, which would alert as a dead publisher
// that never existed.
func TestMetricsHandler_CEPAPeerMetricsEmpty(t *testing.T) {
	metrics.M.ResetPeers()
	t.Cleanup(metrics.M.ResetPeers)

	h := NewMetricsHandler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()

	if strings.Contains(body, `cee_cepa_last_request_unix_seconds{`) {
		t.Errorf("expected no cee_cepa_last_request_unix_seconds series with no peers\nBody:\n%s", body)
	}
	if strings.Contains(body, `remote=""`) {
		t.Errorf("empty remote label in scrape output\nBody:\n%s", body)
	}
	// The label-free drop counter is always present.
	if !strings.Contains(body, "cee_cepa_peers_dropped_total") {
		t.Errorf("cee_cepa_peers_dropped_total missing from scrape output\nBody:\n%s", body)
	}
}
```

Note the exact float rendering: Prometheus text format writes `1700000000` as
`1.7e+09` and `1700000030` as `1.70000003e+09`. If a value renders differently
than asserted, fix the assertion to match the real output — do not change the
metric to suit the test.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/prometheus/ -run TestMetricsHandler_CEPAPeer -v`
Expected: FAIL — `expected "cee_cepa_last_request_unix_seconds{remote=\"10.0.2.250\"} 1.7e+09" in scrape output, not found`.

- [ ] **Step 3: Write the implementation**

Append to `pkg/prometheus/handler.go`:

```go
// The two per-publisher series need a dynamic label set, which
// prometheus.CounterFunc and prometheus.GaugeFunc cannot carry — hence a
// hand-written collector rather than another entry in newRegistry's
// MustRegister list.
var (
	cepaLastRequestDesc = prometheus.NewDesc(
		"cee_cepa_last_request_unix_seconds",
		"Unix timestamp of the last CEPA request received from this publisher, "+
			"whether handshake, event batch, or failed payload. Alert when "+
			"time()-this exceeds several times CEE's HeartBeatIntervalSecs "+
			"(default 10). The publisher is a CEE server, not a NAS Data Mover: "+
			"a Data Mover that stops publishing into a healthy CEE server is not "+
			"visible here.",
		[]string{"remote"}, nil,
	)
	cepaRegistrationsDesc = prometheus.NewDesc(
		"cee_cepa_registrations_total",
		"Total CEPA RegisterRequest handshakes received from this publisher.",
		[]string{"remote"}, nil,
	)
)

// cepaCollector emits the per-publisher CEPA series from a single snapshot
// per scrape, which keeps the two series mutually consistent.
type cepaCollector struct{}

func (cepaCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- cepaLastRequestDesc
	ch <- cepaRegistrationsDesc
}

func (cepaCollector) Collect(ch chan<- prometheus.Metric) {
	for host, p := range metrics.M.PeerSnapshot() {
		ch <- prometheus.MustNewConstMetric(
			cepaLastRequestDesc, prometheus.GaugeValue,
			float64(p.LastRequestUnix), host,
		)
		ch <- prometheus.MustNewConstMetric(
			cepaRegistrationsDesc, prometheus.CounterValue,
			float64(p.Registrations), host,
		)
	}
}
```

In `newRegistry()`, add to the existing `reg.MustRegister(...)` call, after the
`cee_events_truncated_total` entry:

```go
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_cepa_peers_dropped_total",
				Help: "Total CEPA publishers not recorded because the MaxPeers " +
					"cap was reached. Non-zero means the remote label is " +
					"truncated and a real publisher may be missing.",
			},
			func() float64 { return float64(metrics.M.PeersDropped()) },
		),
```

Then, after that `MustRegister` call and before `return reg`:

```go
	reg.MustRegister(cepaCollector{})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/prometheus/ -race -v`
Expected: PASS, including the pre-existing `TestMetricsHandler_AllRequiredMetrics` and `TestBuildInfoMetric`.

- [ ] **Step 5: Verify the empty case actually guards, by mutation**

In `Collect`, replace the range body's guard by emitting a metric
unconditionally before the loop:

```go
	ch <- prometheus.MustNewConstMetric(cepaLastRequestDesc, prometheus.GaugeValue, 0, "")
```

Run: `go test ./pkg/prometheus/ -run TestMetricsHandler_CEPAPeerMetricsEmpty -v`
Expected: FAIL — `empty remote label in scrape output`.

Remove the added line and re-run; expected PASS. Do not commit the mutation.

- [ ] **Step 6: Run the full gate**

Run: `make ci`
Expected: lint, test, build and vuln all clean. If `make lint` reports formatting, run `make format` and re-run.

- [ ] **Step 7: Commit**

```bash
git add pkg/prometheus/handler.go pkg/prometheus/handler_test.go
git commit -m "feat(metrics): expose per-publisher CEPA series on /metrics

cee_cepa_last_request_unix_seconds and cee_cepa_registrations_total, both
labelled by remote, plus a label-free cee_cepa_peers_dropped_total so cap
truncation is visible. A hand-written collector because CounterFunc and
GaugeFunc cannot carry a dynamic label set; one snapshot per scrape keeps
the two series consistent.

Closes #27"
```

---

### Task 4: Documentation

**Files:**
- Modify: `docs/operator-guide.md` (the metrics table at line ~337, and the section following it)
- Modify: `docs/PROMISES.md` (the `## Observability` section, line 58)
- Modify: `docs/requirements.md` (the OBS table; `OBS-09` at line 224 is the highest ID in use)

**Interfaces:**
- Consumes: the test function names committed in Tasks 1–3. `docs-lint.yml` fails CI if a cited test is not a real function, so cite exactly: `TestStore_RecordPeerRequestAt`, `TestStore_RecordPeerRegistration`, `TestStore_RecordPeerRegistrationUnknownPeer`, `TestStore_PeerCap`, `TestStore_ResetPeers`, `TestStore_PeerConcurrency`, `TestPeerHost`, `TestServeHTTP_StampsPeerOnEveryPath`, `TestServeHTTP_StampsPeerWithoutPort`, `TestServeHTTP_CountsRegistrationsOnly`, `TestMetricsHandler_CEPAPeerMetrics`, `TestMetricsHandler_CEPAPeerMetricsEmpty`.
- Produces: requirement IDs `OBS-10`, `OBS-11`, `OBS-12`.

- [ ] **Step 1: Add the metric rows to the operator guide**

In `docs/operator-guide.md`, append to the metrics table (after the `cee_build_info` row):

```markdown
| `cee_cepa_last_request_unix_seconds` | Gauge | Unix timestamp of the last CEPA request from a publisher, labelled `remote`. Stamped on every PUT — handshake, event batch, or failed payload. Alert when `time() - this > 60` |
| `cee_cepa_registrations_total` | Counter | CEPA `RegisterRequest` handshakes received from a publisher, labelled `remote` |
| `cee_cepa_peers_dropped_total` | Counter | Publishers not recorded because the 64-peer cap was reached. Non-zero means a real publisher may be missing from the labelled series above |
```

- [ ] **Step 2: Add the explanatory section to the operator guide**

Immediately after the metrics table, before "Example Prometheus scrape config":

```markdown
### Publisher liveness

`cee_events_received_total` sitting at zero is ambiguous: the NAS may be quiet,
or the publisher may have stopped and every event is being lost. Prometheus's
own `up` does not separate the two — it says this process answers a scrape, not
that anything is still publishing to it. The `cee_cepa_*` series above answer
that question directly:

```yaml
- alert: CEEPublisherSilent
  expr: time() - cee_cepa_last_request_unix_seconds > 60
  for: 2m
  annotations:
    summary: "CEE publisher {{ $labels.remote }} has not sent a request in over a minute"
```

CEE contacts its configured endpoint unprompted every `HeartBeatIntervalSecs`,
which defaults to 10 (`emc_cee_config.xml` on Linux; the equivalent registry
value on Windows). 60s is therefore six missed beats. **Raise this threshold if
you raise `HeartBeatIntervalSecs`.**

**What this does and does not detect.** A `remote` label is a **CEE server**,
not a NAS Data Mover. The publishing chain is Data Movers → CEE server →
cee-exporter, and one CEE server aggregates many Data Movers. These metrics
catch a CEE host that has gone silent. They do **not** catch a Data Mover that
stopped publishing into a CEE server that is still healthy — that path stays
green.

**Cardinality.** The `remote` label is the source host with the ephemeral port
stripped, capped at 64 distinct publishers. Past the cap, new publishers are
not recorded and `cee_cepa_peers_dropped_total` increments — if that counter is
non-zero, the labelled series are incomplete. Peers are never expired, because
deleting the series for a publisher that went dark would destroy exactly the
signal the alert depends on.

To cross-check a publisher against CEE's own view, enable CEE debug logging
(`Debug` and `Verbose` set to 63 in `emc_cee_config.xml`, then restart the
service) and read `/opt/CEEPack/emc_cee_svc.log`.
```

- [ ] **Step 3: Add the PROMISES.md rows**

Append to the `## Observability` table in `docs/PROMISES.md`:

```markdown
| `/metrics` exposes `cee_cepa_last_request_unix_seconds` and `cee_cepa_registrations_total`, both labelled `remote`, plus a label-free `cee_cepa_peers_dropped_total` | `docs/operator-guide.md` metrics table | `pkg/prometheus/handler_test.go::TestMetricsHandler_CEPAPeerMetrics` asserts the exact labelled text-format lines for two distinct publishers; `TestMetricsHandler_CEPAPeerMetricsEmpty` asserts no series and no `remote=""` artefact when no publisher has been seen (CI `ci` job, `make test`) | Verified |
| Every CEPA request stamps its publisher — handshake, event batch, and unparseable payload alike — so a broken-but-alive publisher does not read as dead | `docs/operator-guide.md` "Publisher liveness" | `pkg/server/server_test.go::TestServeHTTP_StampsPeerOnEveryPath` table-drives all three paths; verified by mutation — moving the stamp after the parse check fails the register-request and parse-error subtests (CI `ci` job, `make test`) | Verified |
| The `remote` label is bounded: the ephemeral port is stripped and distinct publishers are capped at 64, with overflow counted rather than silently dropped | `docs/operator-guide.md` "Publisher liveness" (Cardinality) | `pkg/metrics/metrics_test.go::TestStore_PeerCap` asserts the 65th publisher is rejected and `PeersDropped` increments — verified by mutation, removing the cap check fails it with 65 entries; `pkg/server/server_test.go::TestServeHTTP_StampsPeerWithoutPort` asserts three ports on one host collapse to one label value (CI `ci` job, `make test`) | Verified |
| These metrics detect a CEE server that has gone silent, **not** a NAS Data Mover that stopped publishing into a healthy CEE server | `docs/operator-guide.md` "Publisher liveness" (What this does and does not detect) | Nothing tests this — it is a scope limitation of what the transport can observe, stated so the metric is not read as covering more than it does. No test can verify an absence of capability | **Unverified** — documented limitation, not a tested behaviour |
| The Grafana dashboard in `dashboards/cee-exporter.json` renders these metrics correctly | `README.md`, `docs/operator-guide.md` | Nothing renders, lints, or loads this JSON in CI. It was written against the metric names above and checked by eye only | **Unverified** — no CI job validates the dashboard; do not treat its presence alongside verified metrics as evidence it works |
```

- [ ] **Step 4: Add the requirements.md rows**

Append to the OBS table in `docs/requirements.md` (`OBS-09` at line 224 is the
highest ID in use, so these are 10–12):

```markdown
| OBS-10 | Operator can scrape per-publisher CEPA liveness (`cee_cepa_last_request_unix_seconds{remote}`) from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_CEPAPeerMetrics`, `pkg/server/server_test.go::TestServeHTTP_StampsPeerOnEveryPath` |
| OBS-11 | Operator can scrape per-publisher handshake counts (`cee_cepa_registrations_total{remote}`) from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_CEPAPeerMetrics`, `pkg/server/server_test.go::TestServeHTTP_CountsRegistrationsOnly` |
| OBS-12 | The `remote` label is bounded — port stripped, publishers capped at 64, overflow counted in `cee_cepa_peers_dropped_total` | Delivered | `pkg/metrics/metrics_test.go::TestStore_PeerCap`, `pkg/server/server_test.go::TestServeHTTP_StampsPeerWithoutPort`, `pkg/server/server_test.go::TestPeerHost` |
```

- [ ] **Step 5: Verify every cited test name is real**

`docs-lint.yml` fails CI on a citation that names a function that does not
exist. Check each one before committing:

```bash
for fn in TestStore_PeerCap TestStore_RecordPeerRequestAt TestStore_RecordPeerRegistration \
          TestStore_RecordPeerRegistrationUnknownPeer TestStore_ResetPeers TestStore_PeerConcurrency \
          TestPeerHost TestServeHTTP_StampsPeerOnEveryPath TestServeHTTP_StampsPeerWithoutPort \
          TestServeHTTP_CountsRegistrationsOnly TestMetricsHandler_CEPAPeerMetrics \
          TestMetricsHandler_CEPAPeerMetricsEmpty; do
  grep -rq "func $fn(" pkg/ || echo "MISSING: $fn"
done
```

Expected: no output. Any `MISSING:` line must be fixed before committing —
either the test is not written or the citation is wrong.

- [ ] **Step 6: Build the docs**

Run: `make docs`
Expected: `mkdocs build --strict` succeeds. It fails on a single broken
internal link, so check any anchor added above resolves.

- [ ] **Step 7: Commit**

```bash
git add docs/operator-guide.md docs/PROMISES.md docs/requirements.md
git commit -m "docs: record the CEPA publisher metrics and their limits

Operator guide gains the three metric rows, the alert rule, and a section
stating plainly what the metrics do not detect: a Data Mover behind a
healthy CEE server. PROMISES.md carries that limitation and the unbuilt
dashboard as Unverified rather than letting either look proven.

Refs #27"
```

---

### Task 5: Grafana dashboard

**Files:**
- Create: `dashboards/cee-exporter.json`
- Modify: `README.md` (a pointer to the dashboard)

**Interfaces:**
- Consumes: the metric names committed in Task 3 — `cee_cepa_last_request_unix_seconds`, `cee_cepa_registrations_total`, `cee_cepa_peers_dropped_total`, `cee_events_received_total`, `cee_events_written_total`, `cee_events_dropped_total`, `cee_queue_depth`, `cee_last_fsync_unix_seconds`.
- Produces: nothing consumed by later tasks.

**This task ships an Unverified artefact.** No CI job renders, lints, or loads
this JSON. Task 4 already records it as Unverified in `PROMISES.md`; do not
soften that wording, and do not add a claim anywhere that the dashboard is
tested.

- [ ] **Step 1: Write the dashboard**

Create `dashboards/cee-exporter.json`:

```json
{
  "title": "cee-exporter",
  "uid": "cee-exporter",
  "schemaVersion": 39,
  "editable": true,
  "time": { "from": "now-6h", "to": "now" },
  "refresh": "30s",
  "panels": [
    {
      "id": 1,
      "type": "stat",
      "title": "Publisher silence (seconds since last request)",
      "description": "Per CEE server. A CEE server, not a NAS Data Mover: a Data Mover that stops publishing into a healthy CEE server is not visible here.",
      "gridPos": { "h": 6, "w": 12, "x": 0, "y": 0 },
      "targets": [
        {
          "expr": "time() - cee_cepa_last_request_unix_seconds",
          "legendFormat": "{{remote}}",
          "refId": "A"
        }
      ],
      "fieldConfig": {
        "defaults": {
          "unit": "s",
          "thresholds": {
            "mode": "absolute",
            "steps": [
              { "color": "green", "value": null },
              { "color": "yellow", "value": 30 },
              { "color": "red", "value": 60 }
            ]
          }
        },
        "overrides": []
      }
    },
    {
      "id": 2,
      "type": "timeseries",
      "title": "Registrations per publisher",
      "gridPos": { "h": 6, "w": 12, "x": 12, "y": 0 },
      "targets": [
        {
          "expr": "rate(cee_cepa_registrations_total[5m])",
          "legendFormat": "{{remote}}",
          "refId": "A"
        }
      ],
      "fieldConfig": { "defaults": { "unit": "reqps" }, "overrides": [] }
    },
    {
      "id": 3,
      "type": "timeseries",
      "title": "Event throughput",
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 6 },
      "targets": [
        { "expr": "rate(cee_events_received_total[5m])", "legendFormat": "received", "refId": "A" },
        { "expr": "rate(cee_events_written_total[5m])", "legendFormat": "written", "refId": "B" },
        { "expr": "rate(cee_events_dropped_total[5m])", "legendFormat": "dropped", "refId": "C" }
      ],
      "fieldConfig": { "defaults": { "unit": "eps" }, "overrides": [] }
    },
    {
      "id": 4,
      "type": "timeseries",
      "title": "Queue depth",
      "gridPos": { "h": 8, "w": 6, "x": 12, "y": 6 },
      "targets": [{ "expr": "cee_queue_depth", "legendFormat": "depth", "refId": "A" }],
      "fieldConfig": { "defaults": { "unit": "short" }, "overrides": [] }
    },
    {
      "id": 5,
      "type": "stat",
      "title": "Seconds since last fsync",
      "gridPos": { "h": 8, "w": 6, "x": 18, "y": 6 },
      "targets": [
        { "expr": "time() - cee_last_fsync_unix_seconds", "legendFormat": "fsync age", "refId": "A" }
      ],
      "fieldConfig": { "defaults": { "unit": "s" }, "overrides": [] }
    },
    {
      "id": 6,
      "type": "stat",
      "title": "Publishers dropped (label cap reached)",
      "description": "Non-zero means the 64-publisher cap was hit and the labelled series above are incomplete.",
      "gridPos": { "h": 4, "w": 24, "x": 0, "y": 14 },
      "targets": [
        { "expr": "cee_cepa_peers_dropped_total", "legendFormat": "dropped", "refId": "A" }
      ],
      "fieldConfig": {
        "defaults": {
          "unit": "short",
          "thresholds": {
            "mode": "absolute",
            "steps": [
              { "color": "green", "value": null },
              { "color": "red", "value": 1 }
            ]
          }
        },
        "overrides": []
      }
    }
  ]
}
```

- [ ] **Step 2: Verify the JSON parses and every expression names a real metric**

```bash
python3 -m json.tool dashboards/cee-exporter.json > /dev/null && echo "JSON OK"
for m in cee_cepa_last_request_unix_seconds cee_cepa_registrations_total \
         cee_cepa_peers_dropped_total cee_events_received_total cee_events_written_total \
         cee_events_dropped_total cee_queue_depth cee_last_fsync_unix_seconds; do
  grep -rq "\"$m\"" pkg/prometheus/handler.go || echo "MISSING METRIC: $m"
done
```

Expected: `JSON OK` and no `MISSING METRIC:` lines. This checks the names
match the code; it does **not** verify the dashboard renders. Nothing does.

- [ ] **Step 3: Add the README pointer**

In `README.md`, in the section covering metrics, add:

```markdown
A Grafana dashboard for these metrics is in
[`dashboards/cee-exporter.json`](dashboards/cee-exporter.json). It is not
validated by CI — see `docs/PROMISES.md`.
```

- [ ] **Step 4: Run the full gate**

Run: `make ci && make docs`
Expected: both clean.

- [ ] **Step 5: Commit**

```bash
git add dashboards/cee-exporter.json README.md
git commit -m "feat(dashboards): Grafana dashboard for pipeline health

Per-publisher liveness, registration rate, throughput, queue depth, fsync
age, and the label-cap drop counter. Not validated by CI and recorded as
Unverified in PROMISES.md — the metric names are checked against the code,
nothing checks that it renders.

Refs #27"
```

---

## After all tasks

- [ ] Run `make ci` and `make docs` once more on the complete branch.
- [ ] Open the PR against `main`, referencing #27.
- [ ] The `windows`, `evtx-oracle` and `evtx-readback` jobs do not gate merges — `main` is unprotected (#30). Read their results by hand before merging.
