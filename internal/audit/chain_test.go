package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"drool/internal/secrets"
	"drool/internal/store"
	"drool/internal/store/db"
)

func testStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), store.FileName)
	st, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st, p
}

func newWriter(st *store.Store) *Writer { return NewWriter(st, slog.New(slog.DiscardHandler)) }

func record(t *testing.T, w *Writer, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.Record(context.Background(), Event{Action: "test.event", Actor: &System, TargetType: "thing",
			TargetID: fmt.Sprint(i), Details: map[string]any{"n": i, "note": "ünïcode, commas, colons: and newlines\n"}}); err != nil {
			t.Fatal(err)
		}
	}
}

// tamper runs SQL against the audit table with its append-only triggers
// out of the way, as an attacker with write access to the file could.
func tamper(t *testing.T, st *store.Store, stmts ...string) {
	t.Helper()
	all := append([]string{`DROP TRIGGER IF EXISTS audit_no_update`, `DROP TRIGGER IF EXISTS audit_no_delete`}, stmts...)
	for _, s := range all {
		if _, err := st.Writer.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func verify(t *testing.T, st *store.Store, opt VerifyOptions) *Report {
	t.Helper()
	rep, err := Verify(context.Background(), st.Reader, opt)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func kinds(rep *Report) string {
	var k []string
	for _, p := range rep.Problems {
		k = append(k, fmt.Sprintf("%s@%d", p.Kind, p.Seq))
	}
	return strings.Join(k, " ")
}

func TestChainLinksEveryRecord(t *testing.T) {
	st, _ := testStore(t)
	w := newWriter(st)
	record(t, w, 20)
	// A record committed with the change it describes, and one rolled back:
	// the rolled-back one leaves no gap.
	ctx := context.Background()
	if err := st.InTx(ctx, func(q *db.Queries) error {
		emit, err := w.RecordTx(ctx, q, Event{Action: "test.tx", Actor: &System})
		defer emit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.InTx(ctx, func(q *db.Queries) error {
		if _, err := w.RecordTx(ctx, q, Event{Action: "test.rolledback", Actor: &System}); err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("roll back")
	})
	rep := verify(t, st, VerifyOptions{Seen: w.Seen()})
	if !rep.OK {
		t.Fatalf("intact chain failed: %s %s (%s)", rep.Problem, rep.Detail, kinds(rep))
	}
	// 1 start + 20 + 1 committed in a transaction.
	if rep.Records != 22 || rep.HeadSeq != 22 || w.Seen() != 22 || rep.ChainID == "" {
		t.Fatalf("records %d head %d seen %d chain %q", rep.Records, rep.HeadSeq, w.Seen(), rep.ChainID)
	}
	var action string
	st.Reader.QueryRow(`SELECT action FROM audit_events WHERE seq = 1`).Scan(&action)
	if action != ActionChainStart {
		t.Fatalf("first link is %q", action)
	}
}

func TestChainCommitsToRecordsFromBeforeIt(t *testing.T) {
	st, _ := testStore(t)
	// Records written by a DROOL from before the chain (migration 0016).
	for i := 0; i < 3; i++ {
		if _, err := st.Writer.Exec(`INSERT INTO audit_events (id, ts, actor_type, action, result, details_json) VALUES (?, ?, 'system', 'old.event', 'success', '{}')`,
			fmt.Sprintf("aud_00000000000000000000000%03d", i), 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	w := newWriter(st)
	record(t, w, 2)
	rep := verify(t, st, VerifyOptions{})
	if !rep.OK || rep.LegacyRecords != 3 {
		t.Fatalf("ok %v legacy %d: %s", rep.OK, rep.LegacyRecords, rep.Problem)
	}
	tamper(t, st, `UPDATE audit_events SET action = 'old.edited' WHERE id = 'aud_00000000000000000000000001'`)
	if rep := verify(t, st, VerifyOptions{}); rep.OK || kinds(rep) != "legacy@1" {
		t.Fatalf("edited old record: %v %s", rep.OK, kinds(rep))
	}
}

func TestChainDetectsTampering(t *testing.T) {
	box := secrets.NewBoxForTest()
	keyFor := func(v int) ([]byte, bool) {
		k, _, err := box.DeriveKey(CheckpointPurpose, v)
		return k, err == nil
	}
	setup := func(t *testing.T) (*store.Store, *Writer) {
		st, _ := testStore(t)
		w := newWriter(st)
		record(t, w, 5)
		key, v, _ := box.DeriveKey(CheckpointPurpose, 0)
		if ok, err := w.Checkpoint(context.Background(), key, v); !ok || err != nil {
			t.Fatalf("checkpoint: %v %v", ok, err)
		}
		if ok, _ := w.Checkpoint(context.Background(), key, v); ok {
			t.Fatal("a checkpoint right after a checkpoint")
		}
		record(t, w, 5) // seq 1 start, 2–6, 7 checkpoint, 8–12
		rep := verify(t, st, VerifyOptions{CheckpointKey: keyFor, Seen: w.Seen()})
		if !rep.OK || rep.SealedThrough != 6 || rep.Checkpoints != 1 {
			t.Fatalf("intact: ok %v sealed %d checkpoints %d: %s", rep.OK, rep.SealedThrough, rep.Checkpoints, rep.Problem)
		}
		return st, w
	}
	rehash := func(t *testing.T, st *store.Store, from int64) {
		// What an attacker without the master key can do: recompute every
		// hash from a record on, so linkage checks out again.
		t.Helper()
		rows, err := st.Reader.Query(`SELECT ` + columns + ` FROM audit_events WHERE seq IS NOT NULL ORDER BY seq`)
		if err != nil {
			t.Fatal(err)
		}
		var links []*Link
		for rows.Next() {
			l, _, _ := scanLink(rows)
			links = append(links, l)
		}
		rows.Close()
		var stmts []string
		for i, l := range links {
			if l.Seq < from {
				continue
			}
			if i > 0 {
				l.PrevHash = links[i-1].Hash
			}
			l.Hash = l.ComputeHash()
			stmts = append(stmts, fmt.Sprintf(`UPDATE audit_events SET prev_hash = X'%x', hash = X'%x' WHERE seq = %d`, l.PrevHash, l.Hash, l.Seq))
		}
		tamper(t, st, stmts...)
	}
	cases := []struct {
		name   string
		do     func(t *testing.T, st *store.Store)
		seen   bool
		want   string
		prefix bool // want is the first problem; records moved after it are reported too
	}{
		{name: "changed details", want: "changed@4", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `UPDATE audit_events SET details_json = '{"n":99}' WHERE seq = 4`)
		}},
		{name: "changed actor", want: "changed@9", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `UPDATE audit_events SET actor_name = 'someone-else' WHERE seq = 9`)
		}},
		{name: "changed and rehashed", want: "link@5", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `UPDATE audit_events SET result = 'failure' WHERE seq = 4`)
			l := &Link{}
			row := st.Reader.QueryRow(`SELECT ` + columns + ` FROM audit_events WHERE seq = 4`)
			var seq any
			row.Scan(&l.ID, &l.Ts, &l.ActorType, &l.ActorID, &l.ActorName, &l.Action, &l.TargetType, &l.TargetID, &l.Result, &l.SourceIP,
				&l.RequestID, &l.Details, &l.PrevHash, &seq, &l.Hash)
			l.Seq = 4
			tamper(t, st, fmt.Sprintf(`UPDATE audit_events SET hash = X'%x' WHERE seq = 4`, l.ComputeHash()))
		}},
		{name: "deleted in the middle", want: "missing@5", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `DELETE FROM audit_events WHERE seq = 5`)
		}},
		{name: "deleted and renumbered", want: "link@5 changed@6", prefix: true, do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `DELETE FROM audit_events WHERE seq = 5`)
			for s := 6; s <= 12; s++ {
				tamper(t, st, fmt.Sprintf(`UPDATE audit_events SET seq = %d WHERE seq = %d`, s-1, s))
			}
		}},
		{name: "deleted from the end", seen: true, want: "truncated@11", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `DELETE FROM audit_events WHERE seq >= 11`)
		}},
		{name: "reordered", want: "link@3 link@4 link@5", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `UPDATE audit_events SET seq = -1 WHERE seq = 3`, `UPDATE audit_events SET seq = 3 WHERE seq = 4`,
				`UPDATE audit_events SET seq = 4 WHERE seq = -1`)
		}},
		{name: "inserted in the middle", want: "link@6 link@7", prefix: true, do: func(t *testing.T, st *store.Store) {
			for s := 12; s >= 6; s-- {
				tamper(t, st, fmt.Sprintf(`UPDATE audit_events SET seq = %d WHERE seq = %d`, s+1, s))
			}
			tamper(t, st, fmt.Sprintf(`INSERT INTO audit_events (id, ts, actor_type, action, result, seq, prev_hash, hash) VALUES ('aud_forged', 1, 'user', 'forged', 'success', 6, X'%x', X'%x')`,
				sha256.Sum256(nil), sha256.Sum256([]byte("x"))))
		}},
		{name: "added outside the chain", want: "unchained@0", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `INSERT INTO audit_events (id, ts, actor_type, action, result) VALUES ('aud_zzzzzzzzzzzzzzzzzzzzzzzzzz', 1, 'user', 'sneaky', 'success')`)
		}},
		{name: "rewritten, checkpoint left alone", want: "checkpoint@7", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `UPDATE audit_events SET actor_name = 'framed' WHERE seq = 3`)
			rehash(t, st, 3)
		}},
		{name: "rewritten without the key", want: "forged@7", do: func(t *testing.T, st *store.Store) {
			// The careful attacker also points the checkpoint at the new
			// hashes; only the MAC (master key) gives it away.
			tamper(t, st, `UPDATE audit_events SET actor_name = 'framed' WHERE seq = 3`)
			rehash(t, st, 3)
			var h []byte
			st.Reader.QueryRow(`SELECT hash FROM audit_events WHERE seq = 6`).Scan(&h)
			var js string
			st.Reader.QueryRow(`SELECT details_json FROM audit_events WHERE seq = 7`).Scan(&js)
			var cp checkpointDetails
			json.Unmarshal([]byte(js), &cp)
			cp.CoversHash = hex.EncodeToString(h)
			b, _ := json.Marshal(cp)
			tamper(t, st, `UPDATE audit_events SET details_json = '`+string(b)+`' WHERE seq = 7`)
			rehash(t, st, 7)
		}},
		{name: "two separate changes", want: "changed@3 changed@10", do: func(t *testing.T, st *store.Store) {
			tamper(t, st, `UPDATE audit_events SET target_id = 'x' WHERE seq = 3`, `UPDATE audit_events SET target_id = 'y' WHERE seq = 10`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, w := setup(t)
			c.do(t, st)
			opt := VerifyOptions{CheckpointKey: keyFor}
			if c.seen {
				opt.Seen = w.Seen()
			}
			rep := verify(t, st, opt)
			got := kinds(rep)
			if c.prefix && strings.HasPrefix(got, c.want) {
				got = c.want
			}
			if rep.OK || got != c.want {
				t.Fatalf("got ok=%v problems %q, want %q (%s; %s)", rep.OK, kinds(rep), c.want, rep.Problem, rep.Detail)
			}
			if rep.Problem == "" || len(rep.Evidence) == 0 && c.want != "truncated@11" {
				t.Fatalf("no explanation or evidence: %+v", rep)
			}
		})
	}

	t.Run("checkpoint key not available", func(t *testing.T) {
		st, _ := setup(t)
		rep := verify(t, st, VerifyOptions{CheckpointKey: func(int) ([]byte, bool) { return nil, false }})
		if !rep.OK || rep.Unverifiable != 1 || rep.SealedThrough != 0 {
			t.Fatalf("ok %v unverifiable %d sealed %d", rep.OK, rep.Unverifiable, rep.SealedThrough)
		}
	})
	t.Run("another master key", func(t *testing.T) {
		st, _ := setup(t)
		other := secrets.NewBoxForTest()
		rep := verify(t, st, VerifyOptions{CheckpointKey: func(v int) ([]byte, bool) {
			k, _, err := other.DeriveKey(CheckpointPurpose, v)
			return k, err == nil
		}})
		if rep.OK || kinds(rep) != "forged@7" {
			t.Fatalf("got %v %s", rep.OK, kinds(rep))
		}
	})
}

func TestChainConcurrentWriters(t *testing.T) {
	st, p := testStore(t)
	// A second process (the CLI) writing to the same database.
	other, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	w1, w2 := newWriter(st), newWriter(other)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			w := w1
			if g%2 == 1 {
				w = w2
			}
			for i := 0; i < 25; i++ {
				if err := w.Record(context.Background(), Event{Action: "test.concurrent", Actor: &System, TargetID: fmt.Sprint(g, "-", i)}); err != nil {
					t.Error(err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	rep := verify(t, st, VerifyOptions{})
	if !rep.OK || rep.Records != 201 {
		t.Fatalf("ok %v records %d: %s", rep.OK, rep.Records, rep.Problem)
	}
}

// TestCanonicalEncoding pins the documented encoding (README, "Verifying
// the audit export"): receivers must be able to recompute it.
func TestCanonicalEncoding(t *testing.T) {
	l := &Link{Seq: 2, ID: "aud_x", Ts: 1700000000000, ActorType: "user", ActorID: "usr_1", ActorName: "alex", Action: "user.create",
		TargetType: "user", TargetID: "usr_2", Result: "success", SourceIP: "10.0.0.5", RequestID: "req_1", Details: `{"role":"viewer"}`,
		PrevHash: make([]byte, 32)}
	want := "14:drool-audit-v1,64:" + strings.Repeat("0", 64) + ",1:2,5:aud_x,13:1700000000000,4:user,5:usr_1,4:alex,11:user.create,4:user,5:usr_2,7:success,8:10.0.0.5,5:req_1,17:{\"role\":\"viewer\"},"
	if got := string(l.Canonical()); got != want {
		t.Fatalf("canonical\n got %s\nwant %s", got, want)
	}
	sum := sha256.Sum256([]byte(want))
	if hex.EncodeToString(l.ComputeHash()) != hex.EncodeToString(sum[:]) {
		t.Fatal("hash is not SHA-256 of the canonical bytes")
	}
}

func TestStartDetailsAreStable(t *testing.T) {
	b, _ := json.Marshal(chainStart{ChainID: "c", Format: ChainFormat, LegacyRecords: 0, LegacyDigest: "d"})
	if string(b) != `{"chain_id":"c","format":"drool-audit-v1","legacy_records":0,"legacy_digest":"d"}` {
		t.Fatal(string(b))
	}
}
