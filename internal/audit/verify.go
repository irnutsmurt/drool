package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// VerifyOptions configure Verify.
type VerifyOptions struct {
	// CheckpointKey returns the checkpoint key derived from master key
	// version v; ok=false when that version isn't available (a checkpoint
	// can then only be checked structurally). Nil: no keys at all.
	CheckpointKey func(v int) (key []byte, ok bool)
	// Seen is the newest position the verifying process itself committed;
	// a stored chain ending before it lost records from its end.
	Seen int64
	// UpTo stops at this position (0: the head when verification starts).
	UpTo int64
	// Progress is called every batch with the records checked so far.
	Progress func(records int64)
}

// Report is the outcome of verifying the chain.
type Report struct {
	OK             bool
	ChainID        string
	Records        int64 // chained records checked
	LegacyRecords  int64 // records from before the chain (checked through the first link's digest)
	HeadSeq        int64
	SealedThrough  int64 // newest position covered by a checkpoint whose MAC checked out
	Checkpoints    int64
	Unverifiable   int64 // checkpoints whose master key version isn't available
	FirstBadSeq    int64
	Problem        string // the first problem, plain language ("…, and 2 more problems")
	Detail         string // its technical detail
	Problems       []Problem
	ProblemCount   int64 // all problems found (Problems keeps the first maxProblems)
	Evidence       []EvidenceRecord
	UnchainedAfter []string // ids of records written outside the chain after it started
}

// Problem is one place where the chain doesn't check out. Verification
// continues past it: each link is checked against what is stored before it,
// so one changed or missing record is one problem, and a later break isn't
// hidden by an earlier one.
type Problem struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"` // missing, duplicate, link, changed, start, legacy, checkpoint, forged, unchained, truncated
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

const maxProblems = 50

// EvidenceRecord is a stored record around a problem, as found.
type EvidenceRecord struct {
	Seq          *int64 `json:"seq"`
	ID           string `json:"id"`
	Ts           int64  `json:"ts"`
	ActorType    string `json:"actor_type"`
	ActorID      string `json:"actor_id"`
	ActorName    string `json:"actor_name"`
	Action       string `json:"action"`
	TargetType   string `json:"target_type"`
	TargetID     string `json:"target_id"`
	Result       string `json:"result"`
	SourceIP     string `json:"source_ip"`
	RequestID    string `json:"request_id"`
	Details      string `json:"details_json"`
	PrevHash     string `json:"prev_hash"`
	StoredHash   string `json:"stored_hash"`
	ComputedHash string `json:"computed_hash,omitempty"`
}

func evidenceOf(l *Link, seqValid bool, computed []byte) EvidenceRecord {
	e := EvidenceRecord{ID: l.ID, Ts: l.Ts, ActorType: l.ActorType, ActorID: l.ActorID, ActorName: l.ActorName,
		Action: l.Action, TargetType: l.TargetType, TargetID: l.TargetID, Result: l.Result, SourceIP: l.SourceIP,
		RequestID: l.RequestID, Details: l.Details, PrevHash: hex.EncodeToString(l.PrevHash), StoredHash: hex.EncodeToString(l.Hash)}
	if seqValid {
		s := l.Seq
		e.Seq = &s
	}
	if computed != nil {
		e.ComputedHash = hex.EncodeToString(computed)
	}
	return e
}

// checkpointDetails is a checkpoint record's details.
type checkpointDetails struct {
	CoversSeq  int64  `json:"covers_seq"`
	CoversHash string `json:"covers_hash"`
	KeyVersion int    `json:"key_version"`
	MAC        string `json:"mac"`
}

const columns = `id, ts, actor_type, actor_id, actor_name, action, target_type, target_id, result, source_ip, request_id, details_json, prev_hash, seq, hash`

func scanLink(rows *sql.Rows) (*Link, bool, error) {
	var l Link
	var seq sql.NullInt64
	err := rows.Scan(&l.ID, &l.Ts, &l.ActorType, &l.ActorID, &l.ActorName, &l.Action, &l.TargetType, &l.TargetID,
		&l.Result, &l.SourceIP, &l.RequestID, &l.Details, &l.PrevHash, &seq, &l.Hash)
	l.Seq = seq.Int64
	return &l, seq.Valid, err
}

// Verify recomputes the chain stored in d (read-only; any *sql.DB on a
// DROOL database, e.g. a backup's). It never changes anything. A broken
// chain is a Report with OK=false, not an error; errors mean the check
// itself couldn't run.
func Verify(ctx context.Context, d *sql.DB, opt VerifyOptions) (*Report, error) {
	rep := &Report{OK: true}
	fail := func(seq int64, kind, problem, detail string, ev ...EvidenceRecord) {
		rep.ProblemCount++
		if rep.OK {
			rep.OK, rep.FirstBadSeq, rep.Problem, rep.Detail = false, seq, problem, detail
		}
		if len(rep.Problems) < maxProblems {
			rep.Problems = append(rep.Problems, Problem{Seq: seq, Kind: kind, Message: problem, Detail: detail})
			rep.Evidence = append(rep.Evidence, ev...)
		}
	}
	defer func() {
		if rep.ProblemCount > 1 {
			rep.Problem += fmt.Sprintf(" (and %d more problem(s))", rep.ProblemCount-1)
		}
	}()

	var head sql.NullInt64
	if err := d.QueryRowContext(ctx, `SELECT max(seq) FROM audit_events`).Scan(&head); err != nil {
		return nil, fmt.Errorf("read the audit chain: %w", err)
	}
	rep.HeadSeq = head.Int64
	upTo := opt.UpTo
	if upTo == 0 || upTo > rep.HeadSeq {
		upTo = rep.HeadSeq
	}

	// The records written before the chain, in id order, for the first
	// link's digest; and any record written outside the chain after it.
	// The first link names the newest record it covers, so a clock that
	// went backwards can't make an old record look new.
	legacyLast, chained := "", head.Valid
	if chained {
		var js string
		err := d.QueryRowContext(ctx, `SELECT details_json FROM audit_events WHERE seq = 1`).Scan(&js)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		var cs chainStart
		if json.Unmarshal([]byte(js), &cs) == nil {
			legacyLast = cs.LegacyLastID
		}
	}
	digest := newLegacyDigest()
	rows, err := d.QueryContext(ctx, `SELECT `+columns+` FROM audit_events WHERE seq IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var unchained []*Link
	for rows.Next() {
		l, _, err := scanLink(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if chained && l.ID > legacyLast {
			unchained = append(unchained, l)
			rep.UnchainedAfter = append(rep.UnchainedAfter, l.ID)
			continue
		}
		digest.add(l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.LegacyRecords = digest.n
	if !head.Valid {
		// Nothing chained yet (no record written since the upgrade).
		return rep, nil
	}

	var prev *Link
	next := int64(1)
	for next <= upTo {
		rows, err := d.QueryContext(ctx, `SELECT `+columns+` FROM audit_events WHERE seq >= ? AND seq <= ? ORDER BY seq LIMIT 2000`, next, upTo)
		if err != nil {
			return nil, err
		}
		n := 0
		for rows.Next() {
			l, _, err := scanLink(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			n++
			rep.Records++
			checkLink(rep, opt, l, prev, digest, fail)
			prev = l
			next = l.Seq + 1
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if n == 0 {
			// The positions up to upTo can't all be there: a gap at the end.
			fail(next, "missing", fmt.Sprintf("Audit records #%d to #%d are missing.", next, upTo),
				fmt.Sprintf("no stored record with seq in [%d, %d]", next, upTo))
			break
		}
		if opt.Progress != nil {
			opt.Progress(rep.Records)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if len(unchained) > 0 {
		fail(0, "unchained", fmt.Sprintf("%d audit record(s) were added outside the audit chain after it started.", len(unchained)),
			"records with seq NULL and an id after the first link: "+unchained[0].ID, evidenceOf(unchained[0], false, nil))
	}
	if opt.Seen > rep.HeadSeq {
		fail(rep.HeadSeq+1, "truncated", fmt.Sprintf("The newest audit records were removed: the history ends at #%d, but #%d had been written.", rep.HeadSeq, opt.Seen),
			fmt.Sprintf("stored head %d < head written by this controller %d", rep.HeadSeq, opt.Seen))
	}
	return rep, nil
}

func checkLink(rep *Report, opt VerifyOptions, l, prev *Link, digest *legacyDigest, fail func(int64, string, string, string, ...EvidenceRecord)) {
	computed := l.ComputeHash()
	ok := true
	switch {
	case prev == nil && l.Seq != 1:
		fail(1, "missing", fmt.Sprintf("Audit records #1 to #%d are missing.", l.Seq-1), fmt.Sprintf("the first stored link has seq %d", l.Seq),
			evidenceOf(l, true, computed))
		ok = false
	case prev != nil && l.Seq <= prev.Seq:
		fail(l.Seq, "duplicate", fmt.Sprintf("Audit record #%d appears more than once.", l.Seq),
			fmt.Sprintf("seq %d after seq %d", l.Seq, prev.Seq), evidenceOf(prev, true, nil), evidenceOf(l, true, computed))
		ok = false
	case prev != nil && l.Seq != prev.Seq+1:
		fail(prev.Seq+1, "missing", fmt.Sprintf("Audit records #%d to #%d are missing.", prev.Seq+1, l.Seq-1),
			fmt.Sprintf("seq jumps from %d to %d", prev.Seq, l.Seq), evidenceOf(prev, true, nil), evidenceOf(l, true, computed))
		ok = false
	default:
		// Each link is checked against what is stored before it, so a
		// problem here doesn't cascade into the links after it.
		want := zeroHash
		if prev != nil {
			want = prev.Hash
		}
		if !bytes.Equal(l.PrevHash, want) {
			fail(l.Seq, "link", fmt.Sprintf("Audit record #%d doesn't follow the record before it: the record before it was changed, or records were removed, inserted or reordered here.", l.Seq),
				fmt.Sprintf("seq %d prev_hash %x, expected %x", l.Seq, l.PrevHash, want), evidenceOf(l, true, computed))
			ok = false
		}
	}
	if ok && !bytes.Equal(l.Hash, computed) {
		fail(l.Seq, "changed", fmt.Sprintf("Audit record #%d was changed after it was written.", l.Seq),
			fmt.Sprintf("seq %d stored hash %x, computed %x", l.Seq, l.Hash, computed), evidenceOf(l, true, computed))
		ok = false
	}
	if l.Seq == 1 {
		var cs chainStart
		if l.Action != ActionChainStart || json.Unmarshal([]byte(l.Details), &cs) != nil || cs.ChainID == "" {
			if ok {
				fail(1, "start", "The first audit record is not the start of the audit chain.", "seq 1 action "+l.Action, evidenceOf(l, true, computed))
			}
			return
		}
		rep.ChainID = cs.ChainID
		if cs.LegacyRecords != digest.n || cs.LegacyDigest != digest.hex() {
			fail(1, "legacy", fmt.Sprintf("Audit records from before the chain started were changed, removed or added (%d recorded, %d found).", cs.LegacyRecords, digest.n),
				fmt.Sprintf("legacy digest recorded %s, computed %s", cs.LegacyDigest, digest.hex()), evidenceOf(l, true, computed))
		}
		return
	}
	if !ok || prev == nil || l.Action != ActionCheckpoint || l.ActorType != ActorSystem {
		return
	}
	rep.Checkpoints++
	var cp checkpointDetails
	if json.Unmarshal([]byte(l.Details), &cp) != nil || cp.CoversSeq != prev.Seq || cp.CoversHash != hex.EncodeToString(prev.Hash) {
		fail(l.Seq, "checkpoint", fmt.Sprintf("Audit checkpoint #%d doesn't match the records it seals.", l.Seq),
			"checkpoint "+strconv.FormatInt(l.Seq, 10)+" details "+l.Details, evidenceOf(l, true, computed))
		return
	}
	if opt.CheckpointKey == nil {
		rep.Unverifiable++
		return
	}
	key, have := opt.CheckpointKey(cp.KeyVersion)
	if !have {
		rep.Unverifiable++
		return
	}
	mac, err := hex.DecodeString(cp.MAC)
	if err != nil || !bytes.Equal(mac, checkpointMAC(key, rep.ChainID, cp.CoversSeq, prev.Hash)) {
		fail(l.Seq, "forged", fmt.Sprintf("Audit checkpoint #%d is not genuine: the history up to it may have been rewritten.", l.Seq),
			fmt.Sprintf("checkpoint %d MAC does not verify with master key version %d", l.Seq, cp.KeyVersion), evidenceOf(l, true, computed))
		return
	}
	rep.SealedThrough = cp.CoversSeq
}
