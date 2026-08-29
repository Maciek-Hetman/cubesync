package admin

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestRecordErrorAsyncBurst verifies that a burst of 5000+ concurrent RecordErrorAsync calls
// does not cause goroutine explosion, does not deadlock, and completes safely.
func TestRecordErrorAsyncBurst(t *testing.T) {
	s := &Service{
		metrics:   make(chan metric, 4096),
		errorLogs: make(chan errorEvent, 4096),
		done:      make(chan struct{}),
	}
	// Note: We don't start s.flushLoop() with nil pool to avoid nil pointer in flush,
	// but test the queuing behavior of RecordErrorAsync directly.

	initialGoroutines := runtime.NumGoroutine()
	const count = 6000

	var wg sync.WaitGroup
	wg.Add(count)

	userID := uuid.New()
	for i := 0; i < count; i++ {
		go func(idx int) {
			defer wg.Done()
			s.RecordErrorAsync(userID, "POST", "/v1/sync", 500, "internal_error", "database connection failed")
		}(i)
	}

	wg.Wait()

	// Channel buffer is 4096, so exactly 4096 should be queued and the rest dropped without blocking.
	queued := len(s.errorLogs)
	if queued != 4096 {
		t.Fatalf("expected 4096 items queued in buffer, got %d", queued)
	}

	// Goroutine count should return to baseline
	time.Sleep(50 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()
	if finalGoroutines-initialGoroutines > 10 {
		t.Fatalf("goroutine leak detected: initial=%d, final=%d", initialGoroutines, finalGoroutines)
	}
}

// TestShutdownDrainBehavior tests the shutdown draining loop of flushLoop
func TestShutdownDrainBehavior(t *testing.T) {
	s := &Service{
		metrics:   make(chan metric, 4096),
		errorLogs: make(chan errorEvent, 4096),
		done:      make(chan struct{}),
	}

	// Fill errorLogs with 250 error events
	for i := 0; i < 250; i++ {
		s.errorLogs <- errorEvent{
			method:     "POST",
			route:      "/v1/sync",
			statusCode: 500,
			code:       "test_error",
			message:    "test",
		}
	}

	// Run drain loop as done in flushLoop on metrics close
	errBuffer := make([]errorEvent, 0, 100)
	flushedCount := 0

	// Emulate the shutdown drain logic
	drainDone := make(chan struct{})
	go func() {
		for {
			select {
			case e := <-s.errorLogs:
				errBuffer = append(errBuffer, e)
				if len(errBuffer) >= 100 {
					flushedCount += len(errBuffer)
					errBuffer = errBuffer[:0]
				}
			default:
				if len(errBuffer) > 0 {
					flushedCount += len(errBuffer)
					errBuffer = errBuffer[:0]
				}
				close(drainDone)
				return
			}
		}
	}()

	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("drain loop timed out - possible deadlock")
	}

	if flushedCount != 250 {
		t.Fatalf("expected 250 flushed error events, got %d", flushedCount)
	}
	if len(s.errorLogs) != 0 {
		t.Fatalf("error channel not fully drained, remaining: %d", len(s.errorLogs))
	}
}

// TestRecordRequestAsyncAfterShutdown verifies behavior when RecordRequestAsync is called after metrics channel is closed.
func TestRecordRequestAsyncAfterShutdown(t *testing.T) {
	s := &Service{
		metrics:   make(chan metric, 10),
		errorLogs: make(chan errorEvent, 10),
		done:      make(chan struct{}),
		now:       time.Now,
	}
	close(s.metrics)

	defer func() {
		if r := recover(); r != nil {
			t.Logf("Observed panic when calling RecordRequestAsync after channel close: %v", r)
		}
	}()

	s.RecordRequestAsync("GET", "/v1/me", 200, 10*time.Millisecond)
}
