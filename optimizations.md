# Performance Optimizations — gotreesitter

Incremental log of performance inefficiencies identified by first-principles code
analysis (no git history / changelog / upstream diffing). Each entry: location,
mechanism, why it is slow, and the fix. Findings are verified by adversarial
re-analysis before being marked CONFIRMED.

Status legend: 🔬 candidate (under verification) · ✅ confirmed · 🛠️ fix applied · ❌ rejected

---

## Summary table

| # | Area | Location | Category | Impact | Status |
|---|------|----------|----------|--------|--------|
| _(populated as findings are confirmed)_ | | | | | |

---

## Candidates under verification

### R1 — Global mutex on every grammar-cache fetch
- **Location:** `grammars/embedded_loader.go:185` `getEmbeddedLanguageCacheEntry`
- **Mechanism:** Takes the global `embeddedLanguageCacheMu` (a plain `sync.Mutex`)
  unconditionally on every call, even the overwhelmingly common "entry already
  present, no eviction configured" case. The sibling `recordEmbeddedLanguageUse`
  was already given a lock-free fast path (`embeddedLanguageEvictionActive.Load()`)
  precisely because this path is described as running per external token; the
  fetch half never received the symmetric treatment.
- **Impact:** Lock contention under concurrent parsing of the same/embedded
  languages. Impact scales with parallelism.
- **Fix:** Serve the common read via a lock-free path — `sync.RWMutex` with an
  `RLock` fast path, or an `atomic.Pointer` to an immutable copy-on-write map.
- **Status:** 🔬 candidate — hotness of the per-token claim still being verified.

---

_Wave 1 agent findings pending; this document is appended incrementally._
