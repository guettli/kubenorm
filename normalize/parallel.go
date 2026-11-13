package normalize

import (
	"runtime"
	"sync"
)

// parallelFor calls fn(i) for i in [0, n) on up to GOMAXPROCS goroutines and
// returns the error of the lowest index that failed. Results must be stored
// by index to keep the order deterministic.
func parallelFor(n int, fn func(i int) error) error {
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers <= 1 {
		return sequentialFor(n, fn)
	}

	errs := make([]error, n)
	next := make(chan int)

	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			for i := range next {
				errs[i] = fn(i)
			}
		})
	}

	for i := range n {
		next <- i
	}

	close(next)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func sequentialFor(n int, fn func(i int) error) error {
	for i := range n {
		err := fn(i)
		if err != nil {
			return err
		}
	}

	return nil
}

// CloneResources returns a deep copy, so the resources can be processed
// twice (Process modifies objects in place) without parsing again.
func CloneResources(resources []Resource) []Resource {
	out := make([]Resource, len(resources))

	for i, r := range resources {
		out[i] = r
		out[i].Object, _ = deepCopy(r.Object).(map[string]any)
	}

	return out
}
