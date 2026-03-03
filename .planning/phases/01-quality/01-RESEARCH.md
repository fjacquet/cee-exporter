# Phase 1: Quality - Research

**Researched:** 2026-03-02
**Domain:** Go unit testing (stdlib), http.MaxBytesReader bug fix, table-driven tests, concurrent queue testing
**Confidence:** HIGH

<phase_requirements>

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| QUAL-01 | Unit tests cover CEE XML parser (single event, VCAPS batch, malformed input, RegisterRequest detection) | Table-driven tests with XML fixture strings; `encoding/xml` is stdlib, no mocking required |
| QUAL-02 | Unit tests cover CEPA → WindowsEvent mapper (all 6 event types, field propagation) | Table-driven tests over `cepaToEventID` and `accessMaskFor` maps; deterministic, no I/O |
| QUAL-03 | Unit tests cover queue (enqueue, drop on full, drain on stop) | Fake `evtx.Writer`, channel-based synchronization, `context.Background()` for Start |
| QUAL-04 | Unit tests cover GELFWriter payload construction (field presence, GELF 1.1 compliance) | Extract `buildGELF` test via `encoding/json.Unmarshal` on output; no network required |
| QUAL-05 | Fix readBody nil ResponseWriter bug (panic on oversized payload) | Pass `w http.ResponseWriter` to `http.MaxBytesReader` instead of `nil` |
</phase_requirements>

---

## Summary

Phase 1 adds automated tests to a completed but untested Go 1.24 codebase and fixes one confirmed panic. The test stack is entirely Go stdlib — no external test frameworks are needed or advisable given the project's zero-dependency philosophy (only `BurntSushi/toml` and `golang.org/x/sys` are in `go.mod`). All four packages under test (parser, mapper, queue, evtx) are pure Go with no OS-level side effects, making them straightforward to test without build tags or mocking frameworks.

The `readBody` bug in `pkg/server/server.go` is a confirmed nil pointer dereference: `http.MaxBytesReader(nil, r.Body, maxBody)` passes `nil` as the `ResponseWriter`. The Go documentation states that `MaxBytesReader` uses the ResponseWriter to close the connection when the limit is exceeded. Passing `nil` causes a panic when a payload exceeds 64 MiB. The fix is a one-line change: replace `nil` with `w`.

The GELF payload test (QUAL-04) requires special handling: `buildGELF` is an unexported function. The test must either live in `package evtx` (same package, white-box testing) or the function must be exported. White-box testing in `package evtx` is the idiomatic Go approach and avoids changing the public API.

**Primary recommendation:** Use stdlib `testing` package only, table-driven tests with `t.Run`, a fake `evtx.Writer` implementation for queue tests, and `net/http/httptest.ResponseRecorder` for the readBody fix verification. No third-party dependencies.

---

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `testing` | stdlib (Go 1.24) | Test runner, subtests, assertions | Built into Go; `go test ./...` runs it |
| `encoding/xml` | stdlib | XML fixtures in parser tests | Same package the code under test uses |
| `encoding/json` | stdlib | Unmarshal GELF output for validation | Standard JSON; no external dep needed |
| `net/http/httptest` | stdlib | `ResponseRecorder` for handler tests | Canonical Go HTTP test helper |
| `io` | stdlib | `io.NopCloser` for mock request bodies | Converts `strings.Reader` to `ReadCloser` |
| `strings` | stdlib | Build XML fixture strings inline | No file I/O needed for test fixtures |
| `context` | stdlib | `context.Background()` for queue.Start | Required by queue.Start signature |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `sync` | stdlib | WaitGroup in fake writer | Synchronize async queue drain in tests |
| `time` | stdlib | Fixed timestamps for deterministic tests | Avoid time.Now() in test fixtures |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| stdlib `testing` | `github.com/stretchr/testify` | testify adds `require`/`assert` convenience but adds a dependency; project is zero-dep by design |
| stdlib `testing` | `github.com/onsi/gomega` | Same concern; gomega is large and BDD-style, inappropriate for this codebase |
| `httptest.ResponseRecorder` | custom fake `ResponseWriter` | `httptest.ResponseRecorder` is stdlib and captures status/body automatically; use it |

**Installation:**

```bash
# No installation required — all stdlib
go test ./...
```

---

## Architecture Patterns

### Recommended Test File Structure

```
pkg/
├── parser/
│   ├── parser.go
│   └── parser_test.go          # package parser (white-box)
├── mapper/
│   ├── mapper.go
│   └── mapper_test.go          # package mapper (white-box)
├── queue/
│   ├── queue.go
│   └── queue_test.go           # package queue (white-box)
└── evtx/
    ├── writer_gelf.go
    ├── writer.go
    └── writer_gelf_test.go     # package evtx (white-box — needed for buildGELF)
```

### Pattern 1: Table-Driven Tests with t.Run (QUAL-01, QUAL-02)

**What:** Define a slice-of-structs test table; iterate with `t.Run` for named subtests.
**When to use:** Any function with multiple input/output combinations — all four packages here.
**Example:**

```go
// Source: https://go.dev/wiki/TableDrivenTests
func TestParse(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        wantLen int
        wantErr bool
    }{
        {
            name:    "single_event",
            input:   `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType>...`,
            wantLen: 1,
        },
        {
            name:    "vcaps_batch",
            input:   `<EventBatch><CEEEvent>...</CEEEvent><CEEEvent>...</CEEEvent></EventBatch>`,
            wantLen: 2,
        },
        {
            name:    "malformed_input",
            input:   `not xml at all`,
            wantErr: true,
        },
        {
            name:    "empty_payload",
            input:   ``,
            wantErr: true,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Parse([]byte(tt.input), time.Now())
            if tt.wantErr && err == nil {
                t.Errorf("expected error, got nil")
            }
            if !tt.wantErr && err != nil {
                t.Errorf("unexpected error: %v", err)
            }
            if len(got) != tt.wantLen {
                t.Errorf("len(events) = %d, want %d", len(got), tt.wantLen)
            }
        })
    }
}
```

### Pattern 2: Fake Writer for Queue Tests (QUAL-03)

**What:** Implement `evtx.Writer` interface in the test file to capture events without I/O.
**When to use:** Any test involving `queue.Queue` — it requires a `Writer` at construction time.
**Example:**

```go
// Source: Go stdlib interface testing conventions
type fakeWriter struct {
    mu     sync.Mutex
    events []evtx.WindowsEvent
    done   chan struct{}
}

func (f *fakeWriter) WriteEvent(_ context.Context, e evtx.WindowsEvent) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.events = append(f.events, e)
    if f.done != nil {
        f.done <- struct{}{}
    }
    return nil
}

func (f *fakeWriter) Close() error { return nil }

func TestEnqueue(t *testing.T) {
    fw := &fakeWriter{done: make(chan struct{}, 1)}
    q := New(10, 1, fw)
    ctx := context.Background()
    q.Start(ctx)
    defer q.Stop()

    e := evtx.WindowsEvent{EventID: 4663}
    ok := q.Enqueue(e)
    if !ok {
        t.Fatal("enqueue returned false on non-full queue")
    }
    <-fw.done // wait for worker to process
    fw.mu.Lock()
    if len(fw.events) != 1 {
        t.Errorf("want 1 event written, got %d", len(fw.events))
    }
    fw.mu.Unlock()
}
```

### Pattern 3: GELF Payload Validation (QUAL-04)

**What:** Call `buildGELF` (white-box, package `evtx`), unmarshal output with `encoding/json`, check required fields.
**When to use:** Testing JSON serialization without network I/O.
**Example:**

```go
// Source: encoding/json stdlib docs
func TestBuildGELF(t *testing.T) {
    e := WindowsEvent{
        EventID:         4663,
        ProviderName:    "PowerStore-CEPA",
        Computer:        "nas01",
        TimeCreated:     time.Unix(1700000000, 0),
        SubjectUsername: "testuser",
        SubjectDomain:   "DOMAIN",
        ObjectName:      "/share/file.txt",
        AccessMask:      "0x2",
        Accesses:        "WriteData (or AddFile)",
        CEPAEventType:   "CEPP_FILE_WRITE",
    }
    raw, err := buildGELF(e)
    if err != nil {
        t.Fatalf("buildGELF error: %v", err)
    }
    var m map[string]interface{}
    if err := json.Unmarshal(raw, &m); err != nil {
        t.Fatalf("invalid JSON: %v", err)
    }
    requiredFields := []string{
        "version", "host", "short_message", "timestamp", "level",
        "_event_id", "_object_name", "_account_name", "_account_domain",
        "_client_address", "_access_mask", "_cepa_event_type",
    }
    for _, f := range requiredFields {
        if _, ok := m[f]; !ok {
            t.Errorf("missing required GELF field: %s", f)
        }
    }
    if v, _ := m["version"].(string); v != "1.1" {
        t.Errorf("version = %q, want 1.1", v)
    }
}
```

### Pattern 4: Fix readBody Nil ResponseWriter (QUAL-05)

**What:** Pass `w http.ResponseWriter` (the actual handler's ResponseWriter) to `http.MaxBytesReader` instead of `nil`.
**When to use:** The `readBody` function signature must accept `w http.ResponseWriter`.
**Example:**

```go
// Fix: change function signature and call site
// BEFORE (panics if body > 64 MiB):
func readBody(r *http.Request) ([]byte, error) {
    r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
    ...
}

// AFTER (correct):
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
    r.Body = http.MaxBytesReader(w, r.Body, maxBody)
    ...
}
// Call site in ServeHTTP:
body, err := readBody(w, r)
```

Test with `httptest.ResponseRecorder`:

```go
// Source: https://pkg.go.dev/net/http/httptest#ResponseRecorder
func TestReadBodyOversized(t *testing.T) {
    // Build a body larger than 64 MiB
    big := bytes.Repeat([]byte("x"), (64<<20)+1)
    req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(big))
    rec := httptest.NewRecorder()

    _, err := readBody(rec, req)
    if err == nil {
        t.Error("expected error for oversized body, got nil")
    }
    // Must not panic — the test itself proves this
}
```

### Anti-Patterns to Avoid

- **Global state in tests:** The `metrics.M` global singleton is mutated by `queue.Enqueue`. Tests that run in parallel will see races. Either reset `metrics.M` between tests or avoid parallel subtests for queue tests.
- **Testing unexported functions from external package:** `buildGELF` is unexported. Tests MUST be in `package evtx` (same package), not `package evtx_test`.
- **Network connections in GELF tests:** Do NOT call `NewGELFWriter` in unit tests — it dials a real UDP/TCP socket. Test only `buildGELF` directly.
- **Sleeping to wait for goroutines:** Use a channel signal (`done chan struct{}`) in the fake writer rather than `time.Sleep`. Sleep-based synchronization is flaky.
- **Capturing `tt` in parallel subtests:** Go 1.22+ loop variable semantics fix this automatically, but since the project targets Go 1.24, no workaround needed.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| HTTP request body for tests | Custom `io.ReadCloser` struct | `httptest.NewRequest` + `bytes.NewReader` | `httptest.NewRequest` sets all required headers and wraps the body correctly |
| HTTP response capture | Custom struct implementing `ResponseWriter` | `httptest.NewRecorder()` | Built-in, sets status, headers, body buffer |
| XML test fixtures | External `.xml` files | Inline string literals in test table | Keeps tests self-contained; fixtures are short and readable |
| JSON field assertion | `strings.Contains` | `json.Unmarshal` into `map[string]interface{}` | Type-safe, catches numeric/string type mismatches |
| Queue drain synchronization | `time.Sleep(100ms)` | Fake writer with signaling channel + `q.Stop()` | Deterministic; no flakiness |

**Key insight:** All required test infrastructure is stdlib. Adding external test libraries would violate the project's zero-dependency design philosophy visible in `go.mod`.

---

## Common Pitfalls

### Pitfall 1: Testing `buildGELF` from External Package

**What goes wrong:** Test file declares `package evtx_test` and cannot access `buildGELF` (unexported).
**Why it happens:** Default IDE/generator creates `_test` package suffix.
**How to avoid:** Declare `package evtx` (no `_test` suffix) in `writer_gelf_test.go`.
**Warning signs:** `undefined: buildGELF` compile error.

### Pitfall 2: Queue Tests Racing on `metrics.M`

**What goes wrong:** `go test -race ./...` reports a data race on `metrics.M.EventsDroppedTotal`.
**Why it happens:** `metrics.M` is a package-level singleton; all tests in the process share it.
**How to avoid:** Reset `metrics.M.EventsDroppedTotal.Store(0)` before each queue test, or run queue tests sequentially (no `t.Parallel()`).
**Warning signs:** `-race` flag reports race on `sync/atomic` operations.

### Pitfall 3: Nil Panic in `readBody` Under Test

**What goes wrong:** Test passes `nil` as ResponseWriter to verify the fix is correct, but the test itself panics before reaching the assertion.
**Why it happens:** The bug is triggered at runtime inside `http.MaxBytesReader` internals, not at call time.
**How to avoid:** Always use `httptest.NewRecorder()` in tests. Verify the fix by sending an oversized body and confirming no panic occurs (the test completing is the proof).
**Warning signs:** `runtime error: invalid memory address or nil pointer dereference` inside `net/http` internals.

### Pitfall 4: VCAPS Batch Test Using Wrong Root Element

**What goes wrong:** Batch XML test uses `<BatchEvent>` instead of `<EventBatch>` and the batch parser silently falls through to single-event parsing, returning 0 events instead of an error.
**Why it happens:** `rawBatch` struct uses `xml:"EventBatch"` — the root tag must be exact.
**How to avoid:** Use correct Dell CEPA XML structure in test fixtures: `<EventBatch><CEEEvent>...</CEEEvent></EventBatch>`.
**Warning signs:** Test expects `wantLen: 2` but gets `wantErr: true` unexpectedly.

### Pitfall 5: `queue.Stop()` Deadlock in Tests

**What goes wrong:** `q.Stop()` blocks forever because the fake writer's signaling channel is full or the channel is never drained.
**Why it happens:** The fake writer sends to a buffered `done` channel; if the test does not drain it, the worker goroutine blocks.
**How to avoid:** Either use an unbuffered channel with a goroutine receiver, or use a buffered channel sized to the number of expected events.
**Warning signs:** Test hangs indefinitely at `q.Stop()`.

---

## Code Examples

Verified patterns from official sources:

### IsRegisterRequest Test (QUAL-01)

```go
// Source: parser package, white-box test
func TestIsRegisterRequest(t *testing.T) {
    tests := []struct {
        name  string
        input string
        want  bool
    }{
        {"register_request", `<RegisterRequest />`, true},
        {"register_request_with_whitespace", "  <RegisterRequest/>  ", true},
        {"event_payload", `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType></CEEEvent>`, false},
        {"empty", ``, false},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := IsRegisterRequest([]byte(tt.input))
            if got != tt.want {
                t.Errorf("IsRegisterRequest(%q) = %v, want %v", tt.input, got, tt.want)
            }
        })
    }
}
```

### Mapper Test for All 6 Event Types (QUAL-02)

```go
// Source: mapper package, white-box test
func TestMapEventID(t *testing.T) {
    tests := []struct {
        cepaType     string
        wantEventID  int
        wantMask     string
    }{
        {"CEPP_CREATE_FILE",      4663, "0x2"},
        {"CEPP_FILE_READ",        4663, "0x1"},
        {"CEPP_FILE_WRITE",       4663, "0x2"},
        {"CEPP_DELETE_FILE",      4660, "0x10000"},
        {"CEPP_SETACL_FILE",      4670, "0x40000"},
        {"CEPP_CLOSE_MODIFIED",   4663, "0x2"},
    }
    for _, tt := range tests {
        t.Run(tt.cepaType, func(t *testing.T) {
            e := parser.CEPAEvent{EventType: tt.cepaType, Timestamp: time.Now()}
            we := Map(e, "testhost")
            if we.EventID != tt.wantEventID {
                t.Errorf("EventID = %d, want %d", we.EventID, tt.wantEventID)
            }
            if we.AccessMask != tt.wantMask {
                t.Errorf("AccessMask = %q, want %q", we.AccessMask, tt.wantMask)
            }
        })
    }
}
```

### Queue Drop-on-Full Test (QUAL-03)

```go
// Source: queue package, white-box test
func TestDropOnFull(t *testing.T) {
    metrics.M.EventsDroppedTotal.Store(0) // reset global counter
    fw := &fakeWriter{}
    q := New(2, 1, fw) // capacity=2, 1 worker (keep worker blocked)
    // Do NOT start the queue — workers not running means channel fills immediately

    e := evtx.WindowsEvent{EventID: 4663}
    q.ch <- e // fill slot 1 (direct write to bypass Enqueue's select)
    q.ch <- e // fill slot 2

    ok := q.Enqueue(e) // should drop
    if ok {
        t.Error("expected Enqueue to return false on full queue")
    }
    if metrics.M.EventsDroppedTotal.Load() != 1 {
        t.Errorf("dropped total = %d, want 1", metrics.M.EventsDroppedTotal.Load())
    }
}
```

### GELF 1.1 Compliance Check (QUAL-04)

```go
// Source: GELF 1.1 spec https://go2docs.graylog.org/current/getting_in_log_data/gelf.html
// Required fields: version, host, short_message
// Optional: timestamp, level, _additional_fields
func TestGELFVersion(t *testing.T) {
    e := WindowsEvent{CEPAEventType: "CEPP_FILE_WRITE", ObjectName: "/test"}
    raw, _ := buildGELF(e)
    var m map[string]interface{}
    json.Unmarshal(raw, &m)
    if m["version"] != "1.1" {
        t.Errorf("version = %v, want 1.1", m["version"])
    }
    // Verify _additional_fields do not start with underscore followed by id or _host
    if _, ok := m["_id"]; ok {
        t.Error("GELF 1.1: _id field is reserved and must not be set")
    }
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `ioutil.NopCloser` | `io.NopCloser` | Go 1.16 | `ioutil` deprecated; use `io` package directly |
| `tt := tt` capture in subtests | Loop variable captured correctly | Go 1.22 | No workaround needed; project uses Go 1.24 |
| `testing/iotest.ErrReader` | Same — still valid | Stable | Use for simulating read errors in body tests |
| External test library (testify) | stdlib `testing` + `t.Errorf` | Always valid | Project is zero-dep; stdlib is sufficient |

**Deprecated/outdated:**

- `ioutil.NopCloser`: replaced by `io.NopCloser` since Go 1.16; do not use `ioutil`
- `t.Logf` with `t.Fail()` combination: prefer `t.Errorf` which combines both
- `http.MaxBytesReader(nil, ...)`: was never valid; passing `nil` has always been a latent panic risk (panics only when limit is exceeded at runtime)

---

## Open Questions

1. **`buildGELF` exportability**
   - What we know: `buildGELF` is unexported; tests in `package evtx` can access it
   - What's unclear: Whether the planner wants to export it (rename to `BuildGELF`) or keep white-box testing
   - Recommendation: Keep it unexported and test from `package evtx`; this is the idiomatic Go approach

2. **`metrics.M` global reset between queue tests**
   - What we know: `metrics.M` is a package-level singleton, mutated by `Enqueue`
   - What's unclear: Whether queue tests should run sequentially or reset the counter
   - Recommendation: Add `metrics.M.EventsDroppedTotal.Store(0)` at the start of each queue test; do not use `t.Parallel()` for queue tests

3. **Delete/Rename event types in mapper**
   - What we know: QUAL-02 says "all 6 event types" but the mapper has 12 entries (directory variants for most)
   - What's unclear: Whether "6 types" means 6 CEPA event categories (ignoring `_DIRECTORY` variants) or 6 tests total
   - Recommendation: Test all semantically distinct EventID/mask combinations (file+directory variants produce identical EventID); cover: CREATE, READ, WRITE, DELETE, SETACL, CLOSE_MODIFIED as the 6 named in the requirements

---

## Sources

### Primary (HIGH confidence)

- Go official docs <https://pkg.go.dev/net/http#MaxBytesReader> — `ResponseWriter` parameter requirement confirmed; nil causes nil pointer dereference
- Go official docs <https://pkg.go.dev/net/http/httptest> — `ResponseRecorder` and `NewRequest` usage
- Go wiki <https://go.dev/wiki/TableDrivenTests> — table-driven test patterns, `t.Run` usage
- Go blog <https://go.dev/blog/subtests> — subtests and parallel execution
- Source code inspection: `pkg/server/server.go` line 121 — `http.MaxBytesReader(nil, r.Body, maxBody)` confirmed bug

### Secondary (MEDIUM confidence)

- Go blog <https://go.dev/blog/synctest> — `testing/synctest` for concurrent tests (Go 1.24 experimental, Go 1.25 GA); not needed here since fake-writer pattern is simpler
- Go GitHub Issue #14981 <https://github.com/golang/go/issues/14981> — historical MaxBytesReader bug context
- <https://www.glukhov.org/post/2025/11/unit-tests-in-go/> — 2025 Go unit testing structure best practices

### Tertiary (LOW confidence)

- None — all critical claims are verified against official Go documentation

---

## Metadata

**Confidence breakdown:**

- Standard stack: HIGH — all stdlib, verified against Go 1.24 docs
- Architecture: HIGH — directly derived from existing source code structure
- Pitfalls: HIGH — derived from direct code inspection (nil ResponseWriter confirmed) and Go 1.22+ loop semantics
- GELF test approach: HIGH — verified that `buildGELF` is unexported; white-box test is the only option without API changes

**Research date:** 2026-03-02
**Valid until:** 2026-09-02 (stdlib-only, very stable; 6-month horizon)
