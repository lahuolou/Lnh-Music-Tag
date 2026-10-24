package store

import (
	"fmt"
	"sync"
	"testing"
)

func TestConfigConcurrentSetGet(t *testing.T) {
	cfg, err := OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	const workers, writes = 16, 40
	var wg sync.WaitGroup
	errs := make(chan error, workers*writes)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < writes; i++ {
				key := fmt.Sprintf("worker-%02d-key-%02d", worker, i)
				if err := cfg.Set(key, fmt.Sprint(i)); err != nil {
					errs <- err
					return
				}
				if got := cfg.GetDefault(key, ""); got != fmt.Sprint(i) {
					errs <- fmt.Errorf("%s: got %q", key, got)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
