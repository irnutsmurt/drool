package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"drool/internal/store"
	"drool/internal/store/db"
)

// TestRotationStopsAnywhere: a rotation that stops after any step (a
// crash) leaves a key file that opens every stored value, and running it
// again finishes the job.
func TestRotationStopsAnywhere(t *testing.T) {
	for _, stop := range []int{2, 3} {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, store.FileName))
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(dir, KeyFileName)
		box, _, _, _ := LoadOrCreateBox(keyPath)
		svc := NewService(st, box)
		for _, name := range []string{"a", "b", "c"} {
			p, err := svc.Prepare(Input{Name: name, Kind: KindPassword, Value: "pw-" + name})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.InTx(ctx, func(q *db.Queries) error { return svc.InsertTx(ctx, q, p) }); err != nil {
				t.Fatal(err)
			}
		}
		testRotateStopAfter = stop
		_, err = RotateMasterKey(ctx, st.Writer, keyPath, false, time.Now())
		testRotateStopAfter = 0
		if err != errStopped {
			t.Fatalf("stop after %d: %v", stop, err)
		}
		// What the controller would load on its next start opens everything.
		after, _, _, err := LoadOrCreateBox(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBox(ctx, st.Reader, after); err != nil {
			t.Fatalf("stop after %d: the key file doesn't open the values: %v", stop, err)
		}
		res, err := RotateMasterKey(ctx, st.Writer, keyPath, false, time.Now().Add(time.Second))
		if err != nil || res.Total != 3 {
			t.Fatalf("stop after %d, rotating again: %+v %v", stop, res, err)
		}
		final, _, _, _ := LoadOrCreateBox(keyPath)
		if _, err := VerifyBox(ctx, st.Reader, final); err != nil || len(final.keys) != 1 {
			t.Fatalf("stop after %d, final key: %v (%d versions)", stop, err, len(final.keys))
		}
		b, _ := os.ReadFile(res.PreviousKeyFile)
		if prev, _ := ParseKeyFile(b); prev == nil || len(prev.keys) != 2 {
			t.Fatalf("stop after %d: the saved previous key file", stop)
		}
		st.Close()
	}
}
