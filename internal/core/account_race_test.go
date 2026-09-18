package core_test

import (
	"sync"
	"testing"

	"plexo/internal/core"
)

// TestAccountSubscribeCancelRace guards the crash where setState sent on a
// subscriber channel that a concurrent Subscribe cancel had just closed.
// Run under -race; the old code fails here.
func TestAccountSubscribeCancelRace(t *testing.T) {
	a := core.NewAccount()
	const workers = 8
	const iters = 5000

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, cancel := a.Subscribe()
				cancel()
			}
		}()
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				a.ClearCredentials()
			}
		}()
	}
	wg.Wait()
}
