package agentmon

import (
	"encoding/json"
	"sync"

	"drool/internal/monitor"
)

// Event is something worth keeping even when routine telemetry is
// dropped: a service changing state, kernel storage evidence, a probe
// result, a fidelity change, a telemetry gap.
type Event struct {
	T        int64           `json:"t"` // unix seconds
	Kind     string          `json:"kind"`
	Subject  string          `json:"subject,omitempty"`
	Severity string          `json:"severity,omitempty"` // info | warning | critical
	Message  string          `json:"message"`
	Data     json.RawMessage `json:"data,omitempty"`
	BootID   string          `json:"boot_id,omitempty"`
}

// Event kinds.
const (
	EventKernel   = "kernel"
	EventProbe    = "probe"
	EventFidelity = "fidelity"
	EventGap      = "gap"
	EventService  = "service"
)

// MaxEvents bounds the important-event queue. When even that overflows
// (a very long outage with a flapping service), the oldest are dropped
// and counted.
const MaxEvents = 1000

// Outbox holds telemetry until the controller takes it. Routine points
// are bounded by bytes and dropped oldest first; each drop extends a
// gap the controller is told about. The latest state, service and
// storage reports replace their predecessors (only the newest matters).
type Outbox struct {
	mu        sync.Mutex
	limit     int
	points    []monitor.Point
	sizes     []int
	bytes     int
	gaps      []monitor.Gap
	events    []Event
	lostEv    int
	dropped   int
	latest    map[string][]byte // kind → newest report
	latestOrd []string
}

// NewOutbox makes an outbox holding about limit bytes of points.
func NewOutbox(limit int) *Outbox {
	return &Outbox{limit: limit, latest: map[string][]byte{}}
}

// SetLimit changes the bound (a new config).
func (o *Outbox) SetLimit(limit int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.limit = limit
	o.trim()
}

// AddPoint queues a point.
func (o *Outbox) AddPoint(p monitor.Point) {
	b, _ := json.Marshal(p)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.points = append(o.points, p)
	o.sizes = append(o.sizes, len(b))
	o.bytes += len(b)
	o.trim()
}

func (o *Outbox) trim() {
	for o.bytes > o.limit && len(o.points) > 0 {
		p := o.points[0]
		o.points, o.bytes = o.points[1:], o.bytes-o.sizes[0]
		o.sizes = o.sizes[1:]
		o.dropped++
		end := p.T + int64(p.S)
		if n := len(o.gaps); n > 0 && o.gaps[n-1].To >= p.T-int64(p.S) {
			o.gaps[n-1].To = max(o.gaps[n-1].To, end)
		} else {
			o.gaps = append(o.gaps, monitor.Gap{From: p.T, To: end, Reason: "the agent's offline buffer was full; the oldest routine samples were discarded"})
		}
		if len(o.gaps) > 100 { // merge rather than grow
			o.gaps[1].From = o.gaps[0].From
			o.gaps = o.gaps[1:]
		}
	}
}

// AddEvent queues an important event.
func (o *Outbox) AddEvent(e Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.events) >= MaxEvents {
		o.events = o.events[1:]
		o.lostEv++
	}
	o.events = append(o.events, e)
}

// SetLatest replaces the queued report of one kind.
func (o *Outbox) SetLatest(kind string, b []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.latest[kind]; !ok {
		o.latestOrd = append(o.latestOrd, kind)
	}
	o.latest[kind] = b
}

// Bytes is the memory the queued points take, and the limit.
func (o *Outbox) Bytes() (int, int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.bytes, o.limit, o.dropped
}

// Batch is one drained delivery.
type Batch struct {
	Metrics *monitor.Batch
	Events  []Event
	Latest  map[string][]byte
	Order   []string
	LostEv  int
}

// Drain takes everything queued. Undelivered parts go back with Requeue.
func (o *Outbox) Drain(maxPoints int) Batch {
	o.mu.Lock()
	defer o.mu.Unlock()
	b := Batch{Latest: o.latest, Order: o.latestOrd, Events: o.events, LostEv: o.lostEv}
	n := min(maxPoints, len(o.points))
	if n > 0 || len(o.gaps) > 0 {
		b.Metrics = &monitor.Batch{Points: append([]monitor.Point(nil), o.points[:n]...), Gaps: o.gaps}
		for _, s := range o.sizes[:n] {
			o.bytes -= s
		}
		o.points, o.sizes = o.points[n:], o.sizes[n:]
	}
	o.gaps, o.events, o.lostEv = nil, nil, 0
	o.latest, o.latestOrd = map[string][]byte{}, nil
	return b
}

// Requeue puts back what couldn't be sent (the session ended mid-way),
// in front of anything queued since.
func (o *Outbox) Requeue(b Batch) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if b.Metrics != nil {
		var sizes []int
		bytes := 0
		for _, p := range b.Metrics.Points {
			j, _ := json.Marshal(p)
			sizes = append(sizes, len(j))
			bytes += len(j)
		}
		o.points = append(b.Metrics.Points, o.points...)
		o.sizes = append(sizes, o.sizes...)
		o.bytes += bytes
		o.gaps = append(b.Metrics.Gaps, o.gaps...)
	}
	ev := append(b.Events, o.events...)
	if len(ev) > MaxEvents {
		o.lostEv += len(ev) - MaxEvents
		ev = ev[len(ev)-MaxEvents:]
	}
	o.events = ev
	o.lostEv += b.LostEv
	for _, k := range b.Order {
		if _, newer := o.latest[k]; !newer {
			o.latest[k] = b.Latest[k]
			o.latestOrd = append(o.latestOrd, k)
		}
	}
	o.trim()
}
