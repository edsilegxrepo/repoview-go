package util

// OBJECTIVES:
// Provide a contention-free, bounded worker pool primitive shared by single-repo
// page generation and multi-repo portal parallel orchestration.
//
// CORE COMPONENTS:
//   - MaxDefaultWorkers: Global cap (16) preventing file-descriptor and SQLite contention.
//   - ClampWorkers: Computes bounded concurrency from caller preference and item count.
//   - ForEachWorkers: Generic lock-free atomic cursor work-stealing dispatcher.
//   - ForEachConcurrent: Convenience per-item wrapper over ForEachWorkers.
//
// FUNCTIONALITY:
//   - Uses atomic.Int64 index increments instead of shared job channels so workers pull
//     work lock-free and can accumulate thread-local buffers before committing state.
//
// DATA FLOW:
//   items []T -> ClampWorkers -> N worker goroutines polling next() -> workerFn / fn

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// MaxDefaultWorkers is the upper bound applied to automatic or CLI-requested worker pools
// to avoid file-descriptor exhaustion and SQLite lock contention on high-core hosts.
const MaxDefaultWorkers = 16

// ClampWorkers resolves a bounded worker count for a slice of length itemCount.
// When requested <= 0, it defaults to runtime.NumCPU() * 2, clamped to [1, MaxDefaultWorkers].
func ClampWorkers(requested, itemCount int) int {
	if itemCount <= 0 {
		return 0
	}
	w := requested
	if w <= 0 {
		w = runtime.NumCPU() * 2
	}
	if w < 1 {
		w = 1
	}
	if w > MaxDefaultWorkers {
		w = MaxDefaultWorkers
	}
	if w > itemCount {
		w = itemCount
	}
	return w
}

// ForEachWorkers processes items across up to workers goroutines using lock-free atomic indexing,
// invoking workerFn once per goroutine with a work-stealing next() iterator so callers can batch state.
func ForEachWorkers[T any](items []T, workers int, workerFn func(next func() (int, T, bool))) {
	n := ClampWorkers(workers, len(items))
	if n == 0 {
		return
	}

	var nextIdx atomic.Int64
	next := func() (int, T, bool) {
		idx := int(nextIdx.Add(1) - 1)
		if idx >= len(items) {
			var zero T
			return -1, zero, false
		}
		return idx, items[idx], true
	}

	var wg sync.WaitGroup
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerFn(next)
		}()
	}
	wg.Wait()
}

// ForEachConcurrent processes items across up to workers goroutines (default bounded if <= 0).
func ForEachConcurrent[T any](items []T, workers int, fn func(idx int, item T)) {
	ForEachWorkers(items, workers, func(next func() (int, T, bool)) {
		for {
			idx, item, ok := next()
			if !ok {
				return
			}
			fn(idx, item)
		}
	})
}
