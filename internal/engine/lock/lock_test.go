package lock

import (
	"context"
	"testing"
	"time"

	"drool/internal/model"
)

func TestSharedAndExclusive(t *testing.T) {
	m := New()
	r1, ok := m.TryAcquire("h", "j1", model.LockShared)
	r2, ok2 := m.TryAcquire("h", "j2", model.LockShared)
	if !ok || !ok2 {
		t.Fatal("two shared locks should coexist")
	}
	if _, ok := m.TryAcquire("h", "j3", model.LockExclusive); ok {
		t.Fatal("exclusive granted while shared held")
	}
	got := make(chan struct{})
	go func() {
		rel, err := m.Acquire(context.Background(), "h", "j3", model.LockExclusive)
		if err == nil {
			close(got)
			rel()
		}
	}()
	time.Sleep(20 * time.Millisecond)
	// A queued exclusive request blocks new shared requests (FIFO, no starvation).
	if _, ok := m.TryAcquire("h", "j4", model.LockShared); ok {
		t.Fatal("shared lock jumped the queue ahead of a waiting exclusive")
	}
	r1()
	r2()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("exclusive waiter not granted after release")
	}
}

func TestAcquireCancelled(t *testing.T) {
	m := New()
	rel, _ := m.TryAcquire("h", "j1", model.LockExclusive)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := m.Acquire(ctx, "h", "j2", model.LockExclusive); err == nil {
		t.Fatal("expected timeout")
	}
	if h := m.Holders("h"); len(h) != 1 || h[0] != "j1" {
		t.Fatalf("holders %v", h)
	}
	rel()
	if _, ok := m.TryAcquire("h", "j3", model.LockExclusive); !ok {
		t.Fatal("cancelled waiter left the lock blocked")
	}
}
