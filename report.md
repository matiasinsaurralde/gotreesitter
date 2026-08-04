# Performance Optimization Audit — Final Report

**Repository:** `gotreesitter` (a ~332K-LOC pure-Go reimplementation of tree-sitter: a GLR
parser with an incremental-reparse engine, a query engine, an arena allocator, and ~206
embedded grammars).
**Method:** first-principles code analysis only — no git history, changelog, `BENCH.md`, or
upstream diffing was used to locate findings. Work was carried out by an aggressively
parallel multi-agent portfolio (two discovery waves of 5–6 agents each, plus a dedicated
adversarial-verification round), with the root agent synthesizing, cross-checking, and
redirecting between rounds.

The incremental log of every finding lives in **`optimizations.md`**; this report is the
synthesis: what the codebase is, the one pattern that explains almost every finding, what was
applied, and — just as importantly — what was **rejected or corrected** because a concrete
change did not survive audit.

---

## 1. Headline: the inefficiencies are *partial regressions*

This codebase is already heavily and competently optimized: an arena allocator with slab
reuse, `sync.Once`/`atomic.Pointer` lazy initialization, per-P scratch pooling, an ASCII
lexer fast-path, memoized GSS node hashing, dense + binary-search parse tables, and pooled
token sources. Naive inefficiencies are rare.

Instead, **almost every inefficiency found is a _partial regression_: an optimization that is
present in one place and omitted from a structurally identical sibling.** The codebase itself
proves the intended fast form at another call site. Concretely:

| The optimization the codebase already uses… | …that was missing at a sibling site |
|---|---|
| `bytesToStringNoCopy` zero-copy token text (every lexer) | `json_lexer.go` built token text with `string(...)` |
| per-session cached `*Language` (scss, yaml scanners) | html/sql/d scanners re-fetched it per external token |
| lock-free read fast-path (`recordEmbeddedLanguageUse`) | `getEmbeddedLanguageCacheEntry` took a full mutex |
| `allocNodeSliceNoClear` (node slices) | field slices had no `NoClear` variant; 60+ clone sites cleared-then-overwrote |
| stack-local visited set (`gssNodeCanReach`) | `gssNodeUniformByteOffset` allocated two maps per call |
| hoisted `usePendingFullParents()` (GSS reduce path) | the non-GSS reduce path recomputed it 3× |
| `atomic.AddInt64` (`bytesTotal`) | six sibling `WalkStats` counters used a mutex |
| reused `queryMatchBudget` semantics | a fresh budget was heap-allocated per match attempt |
| binary search (`cursor.GotoFirstChildForByte`) | `Node.DescendantForByteRange` linear-scans each level |
| `sync.Once` table memoization on `*Language` (`compactTablesOnce`) | `NewParser` rebuilds ~28 derived tables per construction |

The practical consequence: **most fixes are mechanical — restore the established in-tree
pattern to the site that was missed** — and they are low-risk precisely because the safe form
is already exercised elsewhere in the same package.

---

## 2. What was applied (16 changes, all built + targeted-tested)

Every applied change was adversarially verified for behavior-preservation, compiled, and run
against the most relevant existing tests (the repo's own guard tests for that mechanism). No
repo-wide `go test ./...` was run — `AGENTS.md` warns it OOMs — so verification used targeted
`-run` suites, including `-race` where concurrency changed.

**Allocation / memory**
- **D1** `grammars/json_lexer.go` — build token text with the zero-copy `bytesToStringNoCopy`
  (all 10 `Text=` sites); removes one heap copy per JSON token. JSON was the *only* lexer
  deviating from the contract.
- **B1** `arena.go` + reduce/clone helpers — add `allocFieldIDSliceNoClear` /
  `allocFieldSourceSliceNoClear` and route the core fielded-reduce path and the shared clone
  helpers through the `NoClear` allocators, eliminating a dead `memclr` on every fielded
  reduce.
- **PR-2** `parser_reduce.go` + `tree.go` — B1 follow-through: route the full-overwrite clone
  siblings `cloneNodeInArena` (~60 normalization callers) and
  `cloneFieldIDs/SourcesIntoArena` through `NoClear`. The partial-fill
  `materializeHiddenNodeForAlias` path deliberately keeps clearing.
- **A1** `glr.go` — reuse one scratch-owned cleared cycle-guard map in
  `gssNodesCanMergeWithScratch` instead of allocating two maps per link-pair (this exact class
  of per-call map is documented in the file header as having once cost multiple GB of
  allocation on a C# corpus).
- **E1** `query.go` / `query_reader.go` / `query_match_budget.go` — reuse one
  `queryMatchBudget` per cursor/reader invocation, reset before each attempt, removing a
  per-(pattern,node) heap allocation.

**Concurrency**
- **C1** `grammars/{html,sql,d}_scanner.go` — cache the `*Language` via package `sync.Once`
  instead of a per-external-token global-mutex loader lookup. (Scoped to 3 scanners after
  verification found cobol/blade/just already cache; `d` must be package-level because it is
  stateless.)
- **C2** `grammars/embedded_loader.go` — serve `getEmbeddedLanguageCacheEntry`'s hit path
  under a read lock (`RWMutex`) with a double-checked write only on first insert.
- **C3** `grammars/gateway.go` — accumulate `WalkAndParse`'s six stat counters with atomics
  (as `bytesTotal` already was) and assemble `WalkStats` once after `wg.Wait()`; the per-file
  mutex is gone. Public API unchanged.
- **B3/C4** `arena.go` — size the node-arena pools to `GOMAXPROCS` with floors (8/4, no
  small-box regression) and caps (32/16, bounded retention). The old fixed `maxSize` gave the
  5th+ concurrent parse on a >4-core box zero arena reuse. Independently found by two agents.

**Redundant compute / hoists**
- **A6** `parser_reduce.go` — hoist `usePendingFullParents()` once per reduce (was 3× each in
  two functions; the GSS variant already hoisted it).
- **N5** `tree.go` — hoist `n.fieldIDs()` out of the `ChildByFieldName` loop.

**Incremental parsing**
- **F2** `incremental.go` — compute `wholeSourceIdentical` lazily on first use and memoize,
  instead of an eager whole-buffer `bytes.Equal` on every `reuseCursor.reset()`; `reset()`
  invalidates the memo (the cursor is a pooled field).
- **F1 (partial)** `incremental.go` — `fullRootUndo` uses the now-memoized
  `sourceBytesIdentical()` directly (bit-exact for the whole-root span).

**grep engine (self-contained package)**
- **P1-1** `grep/compile.go` — cache the `*Language → *LangEntry` resolution so `parseSnippet`
  stops copying the entire 206-entry registry and force-loading grammars on every parse.
- **D2 / P1-4** `grep/match.go` — capture text is one alloc+copy (slice source directly),
  `TextOverride` path preserved.
- **D3 / P1-6** `grep/where.go` — where-filters run `bytes.Contains` / `Regexp.Match` on the
  capture bytes instead of reconverting to `string` per result.

---

## 3. What audit *rejected or corrected* — the process earning its keep

The adversarial-verification round changed the outcome on five items. This is the most
important section: several "obvious" wins were wrong, and a naive application would have
introduced silent bugs.

- **A2 — mutate-phase reachability cache → REJECTED (silent parse corruption).** Proposed:
  reuse the GSS merge "can" phase's memoized reachability in the "mutate" phase. Verification
  found three independent unsoundness sources; the fatal one: the can-phase cache reflects a
  **virtual-link-augmented** graph and caches `true` permanently, so against the mutate
  phase's *partial real* graph a stale `true` makes a cycle guard refuse a merge the can-phase
  already approved — a dropped stack link / lost parse-forest ambiguity, i.e. silent
  corruption, not a perf delta. Deferred to a dedicated per-mutation-epoch memo (marginal
  payoff).

- **F1 positional fast path → REJECTED (breaks a real safety guarantee).** Proposed: skip the
  reuse byte-comparison for spans before/after the edit region. Implemented, then reverted
  when `TestReuseCursorTopLevelRejectsChangedFinalRefWithoutMaterialization` failed: the reuse
  guard *intentionally* defends against **inaccurate/degenerate edits** (a `{StartByte:0}` edit
  whose real change is one byte later). Only a real `bytes.Equal` catches those. The eager
  whole-buffer scan it wrongly targeted was still removed via F2's lazy memo.

- **PR-1 — lexer keyword `string()` → NoCopy → REJECTED (not actually an allocation).** A
  hunter agent flagged 9 lexer sites as heap-allocating keyword-lookup keys. `go build
  -gcflags=-m` escape analysis showed **all 9 "do not escape"** — the compiler already avoids
  the heap allocation. Two agents disagreed; the escape-analysis check resolved it. No win.

- **E2 — clone-per-step captures → checkpoint/rollback → DEFERRED.** A genuine allocation win,
  but a semantic rewrite of the query matcher's backtracking core with a *silent-wrong-captures*
  failure mode (the repo even carries a `query_silent_wrong_witness_test.go`). Correctly gated
  on the cgo differential-parity harness rather than applied inline.

- **C1 — scope corrected 6 → 3 scanners.** The finding named six scanners; verification found
  cobol/blade/just already cache their resolved symbols via `sync.Once`. Only html/sql/d
  needed the fix.

Two findings were also *cross-validated* by independent agents arriving from different routes
(the arena-pool cliff, B3=C4; and the memory vs. concurrency views of the same pooling), which
raised confidence before application.

---

## 4. Verified but not applied (recommended follow-ups)

These survived analysis (and, where noted, adversarial verification) but were left unapplied
to bound churn/risk on a core parser within this engagement. Each is documented with mechanism
and fix in `optimizations.md`; the highest-value ones:

- **S1 (High) — `NewParser` rebuilds ~28 immutable `*Language`-derived tables per
  construction.** They are pure functions of the language yet stored per-Parser; the codebase
  already memoizes the analogous `compactTables`/ascii tables on `*Language` via `sync.Once`.
  Per-parse for unpooled callers (`grammars.ParseFile`, `taproot/walk.ParseWithLanguage`) and
  **once per injected region** for highlighting. Fix: hang the tables off a `sync.Once` struct
  on `*Language`; `NewParser` copies pointers. This is the single biggest remaining win — a
  substantial but well-precedented refactor.
- **S3 (Med-High) — `ReadAllGzipWithSizeHint` copies the decompressed grammar stream twice**
  (a 32 KB staging buffer + `append` into an already-pre-sized target). ~308 MB of avoidable
  memcpy on the largest grammar's cold load. Fix: decompress directly into `raw[len:cap]`.
- **N2 (High) — `Node.DescendantForByteRange/PointRange` linear-scan each tree level** where
  `cursor.go` already binary-searches (`sort.Search`). The editor "node under cursor" hot
  path. Fix: reuse the binary search (careful with the point-query and zero-width edge cases).
- **N1 (High) — `NamedChild(i)` is O(n²)** over the standard iteration idiom (both `NamedChild`
  and the `NamedChildCount()` loop condition rescan from 0). Cheap partial fix: hoist
  `NamedChildCount()` out of loop conditions; structural fix: a one-pass `NamedChildren()`.
- **P1-2 (Med-High) — grep's replace pipeline parses the source twice** (`executeMatch` then
  `computeEdits`). Fix: thread the already-parsed root through.
- **P1-3 (Med) — grep `ApplyEdits` is O(edits×N)** (rebuilds the whole buffer per edit). Fix:
  one ascending pass into a pre-sized buffer.
- Lower-value / one-time / tooling: S4, S5, S6 (grammar-load fixups), N3/N4 (traversal
  constant-factors), the ~45 remaining mechanical `NoClear` clone sites, grep P1-5/P1-7, and
  the taproot/wasm/corpuscheck items (P2-1…P2-4), which are error-path or dev-tooling only.

Genuinely clean areas (audited, no action): the core lexer DFA (ASCII fast-path), the
parse-action table lookup (dense + binary search), the per-parse-on-a-warm-parser path (pooled
token sources, `sync.Once` tables), the `TreeCursor` (O(1)/step), and — verified end-to-end —
**regexp usage: there is no per-match/per-token recompilation anywhere**; every `MustCompile`
is package-level and every `Compile` runs once at query/where-compile time.

---

## 5. Method notes

- **Portfolio, not monoculture.** Wave 1 ran six independent families (GLR core, memory/arena,
  concurrency, regexp/strings, query engine, incremental/DFA). Wave 2 redirected toward the
  under-explored families (grammar-load/serialization, tree/cursor traversal, the grep +
  peripheral packages) and a dedicated *partial-regression hunter* that generalized the
  emerging theme. Routes that went dry (e.g. "regexp in a hot loop") were marked blocked and
  not re-staffed.
- **Adversarial verification before commit.** Every concrete change destined for the core was
  handed to a skeptic agent tasked with *refuting* its safety; findings that survived were
  applied, those that didn't were rejected (§3). Disagreements between agents were resolved by
  direct measurement (escape analysis, the repo's own guard tests), never by majority vote.
- **Incremental, reversible delivery.** Work landed on branch `claude/hello-iufgo8` in five
  small, individually-tested commits, with `optimizations.md` updated as the running log. No
  pull request was opened.

**Net:** 16 behavior-preserving performance fixes applied and verified, 4 unsafe/ineffective
changes caught and rejected before landing, and a prioritized backlog of verified follow-ups —
all traceable to a single structural insight about how this codebase regressed.
