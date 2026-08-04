# Performance Optimizations — gotreesitter

First-principles performance audit (no git history / changelog / upstream diffing —
findings derived purely by reading the code). Conducted with a multi-agent portfolio
across six independent approach families, then cross-checked adversarially.

**Central finding — a systematic pattern of _partial regressions_.** This codebase is
heavily and competently optimized (arena allocator, slab reuse, `sync.Once`/`atomic.Pointer`
lazy init, per-P scratch pooling, ASCII lexer fast-path, memoized GSS hashing, dense +
binary-search parse tables). Almost every inefficiency found is not a naive mistake but an
**optimization that was applied in one place and omitted from a structurally identical
sibling** — one lexer that doesn't use the zero-copy contract, some external scanners missing
the per-session cache the others have, a `NoClear` allocator variant that exists for nodes but
not fields, a lock-free fast path added to one half of a pair but not the other, a GSS walk
de-allocated in one function but not its neighbour, a value hoisted in the GSS reduce path but
recomputed in the non-GSS one. The fixes are therefore mostly mechanical: **restore the
established in-tree pattern to the site that was missed.**

Status legend: 🔬 candidate · ✅ verified (analysis) · 🛠️ fix applied, builds & targeted tests pass ·
⚠️ partial (safe part applied, unsafe part rejected) · ❌ rejected

**Applied so far (branch `claude/hello-iufgo8`), each adversarially verified then built + targeted-tested:**
D1 (JSON zero-copy), C1 (html/sql/d scanner lang cache), C3 (WalkAndParse atomics), A1 (GSS merge
map reuse), E1 (query budget reuse), B1 (arena NoClear field slices), A6 (reduce value hoist),
C2 (grammar-cache RWMutex read path), F2 (lazy whole-source compare), F1-partial (`fullRootUndo`
now uses the memoized whole-source compare).

**Adversarial verification changed the outcome on four findings — the process earned its keep:**
- **C1** scope corrected 6→3 scanners (cobol/blade/just already cache via `sync.Once`).
- **E1** reset corrected to clear `trip` as well as `remaining` (a partial reset would silently
  drop matches).
- **E2** (clone-per-step → checkpoint/rollback) deferred: a correctness-critical backtracking
  rewrite with a silent-wrong-captures failure mode, not a mechanical swap. Gate on the cgo parity harness.
- **F1** positional fast path **rejected**: it trusts the declared edit to bound every change, but
  the reuse guard deliberately defends against *inaccurate* edits (a `{StartByte:0}` edit whose real
  change is one byte later — `TestReuseCursorTopLevelRejectsChangedFinalRefWithoutMaterialization`
  fails with the shortcut). Only a real `bytes.Equal` catches those. The eager whole-buffer scan it
  ran every reset was still removed via F2's lazy memo; the descendant-skip propagation the finding
  also proposed is deferred (safe but invasive, gate on the incremental differential suites).

---

## Summary — ranked

| # | Area | Location | Category | Impact | Status |
|---|------|----------|----------|--------|--------|
| **D1** | JSON lexer | `grammars/json_lexer.go` ×8 | zero-copy conversion | **High** | 🛠️ |
| **C1** | External scanners | `html/sql/d/cobol/blade/just` scanners | per-token lock + env-lock | **High** | 🛠️ |
| **F1** | Incremental reuse | `incremental.go:562` | O(bytes×depth) recompare | **High** | ⚠️ partial |
| **A1** | GSS merge | `glr.go:3376` | per-call map alloc (GB-scale) | **Med-High** | 🛠️ |
| **B1** | Arena alloc | `arena.go:1560/1598` + reduce path | dead memclr / missing NoClear | **Med-High** | 🛠️ |
| **E1** | Query match | `query_reader.go:84`, `query.go:877` | per-attempt heap alloc | **Med-High** | 🛠️ |
| **C2** | Grammar cache | `embedded_loader.go:185` | global mutex, no fast path | **Med-High** | 🛠️ |
| **F2** | Incremental reset | `incremental.go:104` | eager whole-buffer Equal | **Med-High** | 🛠️ |
| **E2** | Query match | `query_matcher_generic.go:179` | clone-per-step captures | Med-High | 🔬 |
| **A2** | GSS merge | `glr.go:3366/4171` | uncached reachability re-walk | Med | 🔬 |
| **B3=C4** | Arena pool | `arena.go:313` | pool-size cliff under concurrency | Med | ✅ (2 agents) |
| **C3** | Batch parse | `grammars/gateway.go:216` | mutex where atomics suffice | Med | 🛠️ |
| **B2** | Reduce scratch | `parser_reduce.go:5927` | per-reduce clear only GC-useful | Med | 🔬 |
| **F3** | DFA lexer | `parser_dfa_token_source.go:4506` | contextual-keyword re-scan | Med | 🔬 |
| **F4** | External scan | `parser_dfa_token_source.go:3672` | winning-ELS scanned twice | Med | 🔬 |
| **D4** | grep rewrite | `grep/rewrite.go:208` | O(edits×N) buffer rebuild | Med | 🔬 |
| **E3** | Query index | `query.go:602` | per-node candidate merge alloc | Med | 🔬 |
| **E5** | Query predicate | `query_matcher_generic.go:181` | per-step capture-text realloc | Med | 🔬 |
| **E4** | Query anchors | `query_matcher.go:385` | named-pos rebuild w/o anchors | Med | 🔬 |
| **D2** | grep match | `grep/match.go:183` | double text conversion | Med | 🔬 |
| **D3** | grep where | `grep/where.go:100` | []byte→string per result | Med | 🔬 |
| **A3** | Stack cull | `parser.go:8461` | O(keep×n) + O(m²) sorts | Med-Low | 🔬 |
| **A6** | Reduce | `parser_reduce.go:7276` | value recomputed 3×/reduce | Low (0-risk) | 🛠️ |
| **B4** | Arena reset | `arena.go:897` | 126 discrete stores/reset | Low-Med | 🔬 |
| **A4** | GSS merge | `glr.go:4801` | linear slot scan | Low-Med | 🔬 |
| **B5** | Arena acquire | `arena.go:506` | redundant slab re-walk | Low | 🔬 |
| **D5** | grep rewrite | `grep/rewrite.go:262` | 2K template rescans/match | Low-Med | 🔬 |
| **E6** | Query field | `query_reader.go:161` | string-compare vs FieldID | Low-Med | 🔬 |
| **E7** | Query symbol | `query_reader.go:106` | per-node symbol recompute | Low-Med | 🔬 |
| **F5** | External scan | `parser_dfa_token_source.go:4862` | 4KB serialize to test empty | Low-Med | 🔬 |
| **A5** | GSS equiv | `glr.go:3611` | payload ptr re-derived ×12 | Low | 🔬 |
| **C5** | phase0a (non-prod) | `phase0a_identity.go:347` | diag observer global mutex | Low | 🔬 |

> `regexp` was audited end-to-end: **no per-match/per-token recompilation exists** — all
> `MustCompile` are package-level and all `Compile` run once at query/where-compile time. The
> tempting "regex in a hot loop" class is genuinely absent.

---

## Tier 1 — high-value (fixes being applied)

### D1 — JSON lexer heap-copies every token's text ✅
- **Location:** `grammars/json_lexer.go:339,355,372,385,397,422,440,506,583` (8 sites)
- **Mechanism:** Each token is built with `Text: string(ts.src[a:b])`, a heap allocation + copy
  per token (verified `escapes to heap` via `-gcflags=-m`). Every *other* lexer in the tree
  builds tokens through `makeToken`/`bytesToStringNoCopy` (`token_source_common.go:250`), which
  aliases the source with `unsafe.String` — zero allocation. `json_lexer.go` is the **only**
  lexer using `Text: string(...)` (verified by grep across all `*_lexer.go`/`*_scanner.go`).
  `Node.Text()` re-derives text from byte offsets anyway, so the copy is pure waste.
- **Impact:** High — one heap allocation eliminated per token across every JSON parse; JSON is
  high-volume. Isolated, mechanical, matches the codebase's own established contract.
- **Fix:** Replace `string(ts.src[a:b])` → `bytesToStringNoCopy(ts.src[a:b])` at all 8 sites.

### C1 — Six external scanners re-fetch their language per token through two global locks ✅
- **Location:** `grammars/html_scanner.go:65`, `sql_scanner.go:90`, `d_scanner.go:40`,
  `cobol_scanner.go:35`, `blade_external_scanner.go:41`, `just_scanner.go:33`
- **Mechanism:** Each `Scan()` calls its language accessor (`HtmlLanguage()` …) *solely* to index
  `lang.ExternalSymbols`. That routes through `loadEmbeddedLanguage(...)` on **every external
  token**, paying (a) `os.Getenv` → the Go runtime's `syscall.envLock` RWMutex, and (b)
  `getEmbeddedLanguageCacheEntry` → the global `embeddedLanguageCacheMu` `sync.Mutex`. Under
  concurrent parsing every external-token scan across all goroutines serializes on that mutex.
  The per-session cache fix was already shipped for **SCSS** (`scssScanState{lang}`,
  `scss_scanner.go:29`) and **YAML** (`yaml_scanner.go:288`) — whose own comment measured this
  loader call at **~4% of scss forest parse single-threaded** — but six sibling scanners were
  left unmigrated.
- **Impact:** High — removes a per-token global mutex + env-lock from the concurrent parse path.
- **Fix:** Cache the `*Language` (or its `ExternalSymbols`) in the scanner's per-parse payload,
  mirroring SCSS/YAML. (`d`'s `Create()` returns nil → needs a payload or a package `sync.Once`.)

### F1 — Incremental reuse re-compares each byte once per covering tree level ✅
- **Location:** `incremental.go:447,469-473,562-575` (root-dirty origin `tree.go:4062`)
- **Mechanism:** `Tree.Edit` marks a node dirty when its span *overlaps* the edit; the root spans
  the whole file, so it is always dirty and `underDirty` becomes true for **every** descendant.
  The `underDirty` guard was intended as a cheap pre-filter to *skip* the byte check away from the
  edit, but universal dirtiness defeats it — so `nodeBytesUnchanged` runs a full-span `bytes.Equal`
  on essentially every visited node. Because parent and child spans overlap, the same bytes are
  re-compared at every level: **O(editOffset × depth)**, with the root's edit-child alone ≈ the
  whole file. Same-length edits (the common editor case) get no relief from `bytes.Equal`'s
  length short-circuit.
- **Impact:** High for the canonical incremental workload (small edit in a large/deep tree).
- **Fix:** Give `nodeBytesUnchanged` an O(#edits) fast path using the already-computed edit list
  (span entirely before first edit / after last → equal without comparing; span overlapping an
  edit's new region → unequal without comparing), and thread a "clean-verified" flag down
  `advance()` so descendants of a byte-unchanged node are skipped by containment.

### A1 — GSS merge allocates two fresh maps per feasibility check ✅
- **Location:** `glr.go:3376-3377` (`gssNodesCanMergeWithScratch`), per link-pair from
  `gssMainAddLinkSeenMutate:4186` and `gssMainReplaceWorstEquivalentLinkIfBetterMutate:4255`
- **Mechanism:** The function already receives a live `*glrMergeScratch` yet does
  `make(map[*gssNode]bool)` **twice** per call as the cycle-guard for two
  `gssNodeUniformByteOffset` DFS walks. This file's own header comment (`glr.go:26-31`) records
  that an analogous per-call map accounted for **"3.47 GB of 4.36 GB total allocation"** on a C#
  corpus, and the sibling `gssNodeCanReach` (two functions above) was explicitly rewritten to a
  stack-local visited set — but *this* function was missed. The parallel can-phase already solves
  it via `acquireOffsetSeen` (`glr.go:4121`, one cleared reused map).
- **Impact:** Med-High — 2 heap allocations removed per link comparison on merge-heavy grammars
  (Rust/Dart/C#), cutting GC pressure the codebase is historically GB-scale sensitive to.
- **Fix:** Add a reusable `offsetSeen` to `glrMergeScratch`, `clear()`ed before each walk (both
  walks complete before any re-entrant merge), and thread it into `gssNodeUniformByteOffset`.

### B1 — Clear-then-overwrite: dead `memclr`, plus missing `NoClear` field variants ✅
- **Location:** `arena.go:1560,1598` (`allocFieldIDSlice`/`allocFieldSourceSlice` always clear);
  core reduce path `parser_reduce.go:6119-6122`; 106 clone sites across `parser_result_*.go`
- **Mechanism:** `allocFieldIDSlice`/`allocFieldSourceSlice` unconditionally `clear(out)` and are
  then immediately fully overwritten by `copy()` on the core fielded-reduce path
  (`materializeReduceChildrenFromScratch`), so the `memclr` is 100% wasted. `allocNodeSlice` has
  the same issue at 106 `alloc…; copy(…)` sites — and a `NoClear` variant **already exists for
  nodes** (`allocNodeSliceNoClear`) but was never created for the field slices.
- **Impact:** Med-High — removes a full `memclr` on every fielded reduce and every tree-shaping
  clone for C#/Go/TS/Python/PowerShell/Scala.
- **Fix:** Add `allocFieldIDSliceNoClear`/`allocFieldSourceSliceNoClear`; route the field-clone
  helpers, `materializeReduceChildrenFromScratch`, and the `alloc…;copy` node sites through the
  NoClear variants. **Leave** `tree.go:3260/3330/3569` and `defaultFieldSourcesInArena` clearing —
  those partial-fill and legitimately rely on zeroing.

### E1 — A `*queryMatchBudget` is heap-allocated for every (pattern × node) attempt ✅
- **Location:** `query_reader.go:84,111`; `query.go:877`; `query_matcher_generic.go:234`
- **Mechanism:** `newQueryMatchBudget` returns `&queryMatchBudget{…}` inside the per-candidate
  loop — one escaping heap allocation per pattern per node — even for single-step leaf patterns
  that never call `charge()`. On the reader's root branch its `tripped()` result is never even
  read. For a query whose roots match a common symbol over a large tree this is O(nodes ×
  candidates) tiny allocations, frequently 100% wasted.
- **Impact:** Med-High — highest-frequency allocation on the primary `Execute` path.
- **Fix:** Hoist one `queryMatchBudget` per driver invocation (a `QueryCursor` field + a local in
  `executeQueryWithReader`) and reset it per attempt; attempts are strictly sequential so reuse is
  safe. Optionally extend the existing `singleStepQueryMatch` fast-path to the reader path.

### C2 — Grammar-cache lookup takes a full mutex with no lock-free fast path ✅
- **Location:** `grammars/embedded_loader.go:185-194`
- **Mechanism:** The cache map is written once per blob and thereafter read-only in the default
  config, yet `getEmbeddedLanguageCacheEntry` takes `embeddedLanguageCacheMu.Lock()` (full mutual
  exclusion) on every call — defeating, on the read path, the very `embeddedLanguageEvictionActive
  atomic.Bool` fast-path its sibling `recordEmbeddedLanguageUse` was given. Reached per-token via C1.
- **Impact:** Med-High — makes concurrent grammar fetches parallel; hardens against C1-style regressions.
- **Fix:** Serve the hit path lock-free when `!embeddedLanguageEvictionActive.Load()` via an
  `atomic.Pointer` to a copy-on-write snapshot map (or `RWMutex` `RLock` on the hit path).

### F2 — Eager whole-buffer `bytes.Equal(oldSource,newSource)` at every reuse reset ✅
- **Location:** `incremental.go:104` (compute) → `:583-585` (sole consumer)
- **Mechanism:** `reuseCursor.reset()` unconditionally computes `wholeSourceIdentical =
  bytes.Equal(oldSource, newSource)`, but its only consumers are two `dirtyHere &&
  sourceBytesIdentical()` checks that matter only in the edit-then-undo case (almost always
  false). Same-length edits scan up to the edit offset (O(fileSize)) purely to produce `false`.
- **Impact:** Med-High — avoidable full pass on the hot incremental reset for same-length edits.
- **Fix:** Make it lazy/tri-state (compute+memoize on first `sourceBytesIdentical()` call), or gate
  the full compare behind a cheap necessary condition (equal length **and** edited regions equal).

---

## Tier 2 — solid medium-impact _(see scratchpad findings for full mechanism/fix)_

- **E2** clone-per-step captures → checkpoint/rollback + copy-on-emit (`query_matcher_generic.go:179`).
- **A2** thread the can-phase `reachCache` into the merge mutate path (`glr.go:3366/4171`).
- **B3 = C4** (cross-validated by two agents) size `nodeArenaPool.maxSize` to `GOMAXPROCS` or back it
  with `sync.Pool`, preserving the `maxRetainedFullArenaBytes` eviction guard (`arena.go:313`).
- **C3** make the six `WalkAndParse` stat counters `atomic.Int64` (as `bytesTotal` already is) and
  drop the mutex (`grammars/gateway.go:216`).
- **B2** drop `clear(s.nodes)` from `reduceBuildScratch.reset()`; rely on the once-per-parse clear in
  `releaseParserScratch` (`parser_reduce.go:5927`).
- **F3** thread the raw span from the first DFA scan into `promoteActiveLiteralForCurrentState` to
  avoid a second identical DFA walk per contextual keyword (`parser_dfa_token_source.go:4506`).
- **F4** serialize the winning ELS scanner state inside the scoring loop instead of re-running the
  scanner after it (`parser_dfa_token_source.go:3672`).
- **D4** rebuild grep source in a single ascending pass instead of O(edits×N) (`grep/rewrite.go:208`).
- **E3/E4/E5** fold `rootRepetitionPostPatterns` at compile time; skip named-position scan when a
  pattern has no anchors; memoize capture text within a match attempt.
- **D2/D3** grep: avoid the double `string→[]byte` capture conversion and the per-result reconversion
  (`re.Match`/`bytes.Contains` on the bytes already in hand).

## Tier 3 — low / constant-factor

- **A6** hoist `usePendingFullParents()` (recomputed 3× per reduce; the GSS variant already hoists) ✅.
- **B4** group arena diagnostic counters into one struct → single `memclr` instead of 126 stores.
- **A4** index merge slots with `map[glrMergeKey]int` instead of a linear scan.
- **A3** partial-sort/heap the top-K stack cull instead of O(keep×n).
- **B5** lightweight `clearBudget` on arena acquire (skip the redundant slab re-walk).
- **D5** single-pass template substitution instead of 2K `ReplaceAll` scans.
- **E6/E7** compare `FieldID`s instead of field-name strings; compute a node's public symbol once.
- **F5** add an `IsQuiescent()`/`SerializedLen()` fast path instead of a 4 KB serialize-to-measure.
- **A5** resolve a `stackEntry` payload base once per operand instead of ~12×.
- **C5** shard the phase0a diagnostic observer per-`*Core` (non-production build only).

---

_Fixes are applied incrementally on branch `claude/hello-iufgo8`, each verified with `go build`
and targeted tests. Rejected candidates and adversarial-verification notes are recorded in the
final `report.md`._
