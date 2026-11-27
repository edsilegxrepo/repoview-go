package util

import (
	"runtime"
	"sync"
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

// ForEachConcurrent processes items across up to workers goroutines (default bounded if <= 0).
func ForEachConcurrent[T any](items []T, workers int, fn func(idx int, item T)) {
	n := ClampWorkers(workers, len(items))
	if n == 0 {
		return
	}

	type job struct {
		idx  int
		item T
	}

	jobs := make(chan job, len(items))
	for i, it := range items {
		jobs <- job{idx: i, item: it}
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				fn(j.idx, j.item)
			}
		}()
	}
	wg.Wait()
}
