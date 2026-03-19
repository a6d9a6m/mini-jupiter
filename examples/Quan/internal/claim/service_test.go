package claim

import (
	"sync"
	"testing"
)

func TestClaimReservationID_IsUniqueUnderConcurrency(t *testing.T) {
	const total = 10000

	seen := make(map[string]struct{}, total)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	wg.Add(total)
	for i := 0; i < total; i++ {
		go func() {
			defer wg.Done()
			id := claimReservationID()
			if id == "" {
				t.Error("expected non-empty reservation id")
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if _, exists := seen[id]; exists {
				t.Errorf("duplicate reservation id generated: %s", id)
				return
			}
			seen[id] = struct{}{}
		}()
	}
	wg.Wait()
}
