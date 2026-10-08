package monsvc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"drool/internal/monitor"
)

// Tiers of metric history.
const (
	TierRaw  = 0 // agent minutes, SSH polls: one day
	Tier5m   = 1 // eight days
	Tier1h   = 2 // the retention period
	rawKeep  = 26 * time.Hour
	fiveKeep = 8*24*time.Hour + time.Hour
)

var tierSpan = map[int]int64{Tier5m: 300, Tier1h: 3600}

// layouts caches series-key layouts (id ↔ keys).
type layouts struct {
	mu   sync.Mutex
	byH  map[string]int64
	keys map[int64][]string
}

// reset forgets cached ids (after a failed transaction, an id may not exist).
func (l *layouts) reset() {
	l.mu.Lock()
	l.byH, l.keys = map[string]int64{}, map[int64][]string{}
	l.mu.Unlock()
}

func newLayouts() *layouts { return &layouts{byH: map[string]int64{}, keys: map[int64][]string{}} }

func layoutHash(keys []string) string {
	h := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	return hex.EncodeToString(h[:12])
}

// id returns (creating) the layout for sorted keys.
func (l *layouts) id(ctx context.Context, tx *sql.Tx, keys []string) (int64, error) {
	h := layoutHash(keys)
	l.mu.Lock()
	if id, ok := l.byH[h]; ok {
		l.mu.Unlock()
		return id, nil
	}
	l.mu.Unlock()
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM mon_layouts WHERE hash = ?`, h).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		kj, _ := json.Marshal(keys)
		res, err := tx.ExecContext(ctx, `INSERT INTO mon_layouts (hash, keys_json) VALUES (?, ?)`, h, string(kj))
		if err != nil {
			return 0, err
		}
		id, _ = res.LastInsertId()
	} else if err != nil {
		return 0, err
	}
	l.mu.Lock()
	l.byH[h], l.keys[id] = id, keys
	l.mu.Unlock()
	return id, nil
}

// lookup returns a layout's keys.
func (l *layouts) lookup(ctx context.Context, q *sql.DB, id int64) ([]string, error) {
	l.mu.Lock()
	k, ok := l.keys[id]
	l.mu.Unlock()
	if ok {
		return k, nil
	}
	var kj string
	if err := q.QueryRowContext(ctx, `SELECT keys_json FROM mon_layouts WHERE id = ?`, id).Scan(&kj); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(kj), &k); err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.keys[id] = k
	l.mu.Unlock()
	return k, nil
}

// MaxSeriesPerPoint bounds a stored point (an agent sends at most 512).
const MaxSeriesPerPoint = 512

// encode packs a point's values in key order: float32 avg, float32 max.
func encode(v map[string][2]float64) ([]string, []byte) {
	keys := make([]string, 0, len(v))
	for k := range v {
		if len(k) <= 200 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > MaxSeriesPerPoint {
		keys = keys[:MaxSeriesPerPoint]
	}
	b := make([]byte, 8*len(keys))
	for i, k := range keys {
		binary.LittleEndian.PutUint32(b[8*i:], math.Float32bits(float32(v[k][0])))
		binary.LittleEndian.PutUint32(b[8*i+4:], math.Float32bits(float32(v[k][1])))
	}
	return keys, b
}

func decode(keys []string, b []byte) map[string][2]float64 {
	out := make(map[string][2]float64, len(keys))
	for i, k := range keys {
		if 8*i+8 > len(b) {
			break
		}
		a := math.Float32frombits(binary.LittleEndian.Uint32(b[8*i:]))
		m := math.Float32frombits(binary.LittleEndian.Uint32(b[8*i+4:]))
		if math.IsNaN(float64(a)) {
			continue
		}
		out[k] = [2]float64{float64(a), float64(m)}
	}
	return out
}

// validPoint rejects nonsense before storage.
func validPoint(p monitor.Point, now time.Time) bool {
	if p.S < 10 || p.S > 3600 || p.T <= 0 || p.T > now.Unix()+300 || p.T < now.Add(-rawKeep).Unix() || len(p.V) == 0 {
		return false
	}
	for k, v := range p.V {
		if k == "" || math.IsNaN(v[0]) || math.IsInf(v[0], 0) || math.IsNaN(v[1]) || math.IsInf(v[1], 0) {
			delete(p.V, k)
		}
	}
	return len(p.V) > 0
}

// storePointsTx writes raw points.
func (s *Service) storePointsTx(ctx context.Context, tx *sql.Tx, hostID string, ps []monitor.Point) error {
	for _, p := range ps {
		keys, data := encode(p.V)
		id, err := s.layouts.id(ctx, tx, keys)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO mon_points (host_id, tier, ts, span, n, layout, data) VALUES (?, 0, ?, ?, ?, ?, ?)`,
			hostID, p.T, p.S, max(p.N, 1), id, data); err != nil {
			return err
		}
	}
	return nil
}

// --- rollups ---

type rowPoint struct {
	ts, span, n int64
	v           map[string][2]float64
}

func (s *Service) readTier(ctx context.Context, hostID string, tier int, from, to int64) ([]rowPoint, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT ts, span, n, layout, data FROM mon_points WHERE host_id = ? AND tier = ? AND ts >= ? AND ts < ? ORDER BY ts`,
		hostID, tier, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rowPoint
	for rows.Next() {
		var r rowPoint
		var layout int64
		var data []byte
		if err := rows.Scan(&r.ts, &r.span, &r.n, &layout, &data); err != nil {
			return nil, err
		}
		keys, err := s.layouts.lookup(ctx, s.d.Store.Reader, layout)
		if err != nil {
			continue
		}
		r.v = decode(keys, data)
		out = append(out, r)
	}
	return out, rows.Err()
}

// aggregate merges points: time-weighted averages, maxima of maxima.
func aggregate(ps []rowPoint) (map[string][2]float64, int64) {
	sum, wt, mx := map[string]float64{}, map[string]float64{}, map[string]float64{}
	var n int64
	for _, p := range ps {
		w := float64(p.span)
		n += p.n
		for k, v := range p.v {
			sum[k] += v[0] * w
			wt[k] += w
			if cur, ok := mx[k]; !ok || v[1] > cur {
				mx[k] = v[1]
			}
		}
	}
	out := make(map[string][2]float64, len(sum))
	for k := range sum {
		out[k] = [2]float64{sum[k] / wt[k], mx[k]}
	}
	return out, n
}

// rollup computes closed windows of tier from the tier below, from the
// host's watermark. Late points lower the watermark (markLate), so
// windows are recomputed when an agent's buffered data arrives.
func (s *Service) rollup(ctx context.Context, hostID string, tier int, now time.Time) error {
	span := tierSpan[tier]
	s.mu.Lock()
	wm, ok := s.watermarks[hostID][tier]
	if late, isLate := s.late[hostID][tier]; isLate {
		if !ok || late < wm {
			wm, ok = late, true
		}
		delete(s.late[hostID], tier)
	}
	s.mu.Unlock()
	if !ok {
		var last sql.NullInt64
		s.d.Store.Reader.QueryRowContext(ctx, `SELECT MAX(ts) FROM mon_points WHERE host_id = ? AND tier = ?`, hostID, tier).Scan(&last)
		if last.Valid {
			wm = last.Int64 + span
		} else {
			var first sql.NullInt64
			s.d.Store.Reader.QueryRowContext(ctx, `SELECT MIN(ts) FROM mon_points WHERE host_id = ? AND tier = ?`, hostID, tier-1).Scan(&first)
			if !first.Valid {
				return nil
			}
			wm = first.Int64 / span * span
		}
	}
	// A window closes once its end is a little in the past (late minutes).
	end := (now.Unix() - 120) / span * span
	if wm >= end {
		s.setWatermark(hostID, tier, wm)
		return nil
	}
	pts, err := s.readTier(ctx, hostID, tier-1, wm, end)
	if err != nil {
		return err
	}
	groups := map[int64][]rowPoint{}
	for _, p := range pts {
		w := p.ts / span * span
		groups[w] = append(groups[w], p)
	}
	if len(groups) > 0 {
		err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
			for w, g := range groups {
				v, n := aggregate(g)
				keys, data := encode(v)
				id, err := s.layouts.id(ctx, tx, keys)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO mon_points (host_id, tier, ts, span, n, layout, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					hostID, tier, w, span, n, id, data); err != nil {
					return err
				}
				if tier == Tier5m {
					s.markLate(hostID, Tier1h, w)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	s.setWatermark(hostID, tier, end)
	return nil
}

func (s *Service) setWatermark(hostID string, tier int, v int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watermarks[hostID] == nil {
		s.watermarks[hostID] = map[int]int64{}
	}
	s.watermarks[hostID][tier] = v
}

// markLate records data that arrived for a window already rolled up (an
// agent's buffered minutes after an outage): the next pass recomputes
// from there.
func (s *Service) markLate(hostID string, tier int, ts int64) {
	span := tierSpan[tier]
	w := ts / span * span
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, rolled := s.watermarks[hostID][tier]
	if !rolled || w >= cur {
		return
	}
	if s.late[hostID] == nil {
		s.late[hostID] = map[int]int64{}
	}
	if l, ok := s.late[hostID][tier]; !ok || w < l {
		s.late[hostID][tier] = w
	}
}

// Rollups runs one pass over every host.
func (s *Service) Rollups(ctx context.Context) error {
	ids, err := s.hostsWithPoints(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, h := range ids {
		if err := s.rollup(ctx, h, Tier5m, now); err != nil {
			return err
		}
		if err := s.rollup(ctx, h, Tier1h, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) hostsWithPoints(ctx context.Context) ([]string, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id FROM mon_current`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		rows.Scan(&h)
		out = append(out, h)
	}
	return out, rows.Err()
}

// PruneResult reports one prune.
type PruneResult struct {
	At            time.Time `json:"at"`
	Points        int64     `json:"points"`
	Events        int64     `json:"events"`
	ServiceEvents int64     `json:"service_events"`
	Issues        int64     `json:"issues"`
	Millis        int64     `json:"ms"`
}

// Prune drops history beyond each tier's horizon and old events, host by
// host in small transactions so ingestion never waits long.
func (s *Service) Prune(ctx context.Context) PruneResult {
	start := s.now()
	r := PruneResult{At: start}
	set := s.Settings(ctx)
	ret := time.Duration(set.RetentionDays) * 24 * time.Hour
	hosts, _ := s.hostsWithPoints(ctx)
	exec := func(q string, args ...any) int64 {
		var n int64
		s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, q, args...)
			if err == nil {
				n, _ = res.RowsAffected()
			}
			return err
		})
		return n
	}
	for _, h := range hosts {
		for tier, keep := range map[int]time.Duration{TierRaw: rawKeep, Tier5m: fiveKeep, Tier1h: ret} {
			r.Points += exec(`DELETE FROM mon_points WHERE host_id = ? AND tier = ? AND ts < ?`, h, tier, start.Add(-keep).Unix())
		}
	}
	cut := start.Add(-ret).UnixMilli()
	for {
		n := exec(`DELETE FROM mon_events WHERE id IN (SELECT id FROM mon_events WHERE ts < ? LIMIT 5000)`, cut)
		r.Events += n
		if n < 5000 {
			break
		}
	}
	for {
		n := exec(`DELETE FROM mon_service_events WHERE id IN (SELECT id FROM mon_service_events WHERE ts < ? LIMIT 5000)`, cut)
		r.ServiceEvents += n
		if n < 5000 {
			break
		}
	}
	r.Issues = exec(`DELETE FROM mon_issues WHERE state = 'resolved' AND resolved_at < ?`, cut)
	exec(`DELETE FROM mon_gaps WHERE to_ts < ?`, start.Add(-ret).Unix())
	// Per-host caps keep a flapping service or a failing disk from
	// filling the database inside the retention period.
	for _, h := range hosts {
		r.ServiceEvents += exec(`DELETE FROM mon_service_events WHERE host_id = ? AND id NOT IN (SELECT id FROM mon_service_events WHERE host_id = ? ORDER BY id DESC LIMIT ?)`,
			h, h, MaxServiceEventsPerHost)
		r.Events += exec(`DELETE FROM mon_events WHERE host_id = ? AND id NOT IN (SELECT id FROM mon_events WHERE host_id = ? ORDER BY id DESC LIMIT ?)`,
			h, h, MaxEventsPerHost)
	}
	r.Millis = s.now().Sub(start).Milliseconds()
	s.mu.Lock()
	s.lastPrune = r
	s.mu.Unlock()
	return r
}

// Per-host caps on event history.
const (
	MaxServiceEventsPerHost = 20000
	MaxEventsPerHost        = 20000
)
