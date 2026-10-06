package poller

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"drool/internal/engine/lock"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Throughput must be limited by the workers, not the scheduler tick: when a
// worker finishes, the next due task starts right away.
func TestDispatchesAsWorkersFree(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	const n = 24
	for i := 0; i < n; i++ {
		if _, err := st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at)
			VALUES (?, ?, '10.0.0.1', 'deploy', 'ready', 0, 0)`, fmt.Sprintf("h%02d", i), fmt.Sprintf("h%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	p := New(st, nil, lock.New(), func() bool { return false }, slog.New(slog.DiscardHandler), Config{Workers: 4, Tick: time.Hour})
	var mu sync.Mutex
	ran := map[string]bool{}
	done := make(chan struct{})
	var once sync.Once
	p.exec = func(_ context.Context, h db.Host, task string) {
		time.Sleep(20 * time.Millisecond)
		p.reschedule(h.ID, task, time.Hour)
		mu.Lock()
		ran[h.ID] = true
		if len(ran) == n {
			once.Do(func() { close(done) })
		}
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("only %d of %d hosts polled; the scheduler waited for its tick", len(ran), n)
	}
}

// A new host gets its facts and status right away, and no update check runs
// before its first status exists (update counts are stored with it).
func TestNewHostSchedule(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := New(st, nil, lock.New(), func() bool { return false }, slog.New(slog.DiscardHandler), Config{})
	now := time.Now()
	d := p.seed(context.Background(), "h1", now)
	for _, task := range []string{taskFacts, taskStatus} {
		if d[task].After(now) {
			t.Errorf("%s for a new host scheduled at +%s, want now", task, d[task].Sub(now))
		}
	}
	p.due["h1"] = d
	p.run(context.Background(), db.Host{ID: "h1", Name: "h1"}, taskUpdates)
	if d[taskStatus].After(time.Now()) || !d[taskUpdates].After(now) {
		t.Fatalf("an update check without a status row should defer to the status check: status %s, updates %s",
			d[taskStatus].Sub(now), d[taskUpdates].Sub(now))
	}
}
