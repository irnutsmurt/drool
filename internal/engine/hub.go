package engine

import (
	"encoding/json"
	"sync"
	"time"
)

// Event is one server-sent event for a job.
type Event struct {
	ID   int64
	Type string // job | host | output | end
	Data json.RawMessage
}

// Replay buffer bounds per job.
const (
	hubMaxEvents = 2000
	hubMaxBytes  = 2 << 20
	hubLinger    = 5 * time.Minute // keep a finished job's stream for late subscribers
	subBuffer    = 512
)

// Hub fans job events out to SSE subscribers and keeps a bounded replay
// buffer so reconnecting clients can resume with Last-Event-ID.
type Hub struct {
	mu   sync.Mutex
	jobs map[string]*jobStream
}

type jobStream struct {
	next  int64
	ring  []Event
	bytes int
	subs  map[chan Event]struct{}
	ended bool
}

func NewHub() *Hub { return &Hub{jobs: map[string]*jobStream{}} }

func (h *Hub) stream(job string) *jobStream {
	s := h.jobs[job]
	if s == nil {
		s = &jobStream{subs: map[chan Event]struct{}{}}
		h.jobs[job] = s
	}
	return s
}

// Publish sends an event to all subscribers of job.
func (h *Hub) Publish(job, typ string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.stream(job)
	s.next++
	ev := Event{ID: s.next, Type: typ, Data: b}
	s.ring = append(s.ring, ev)
	s.bytes += len(b)
	for len(s.ring) > hubMaxEvents || s.bytes > hubMaxBytes {
		s.bytes -= len(s.ring[0].Data)
		s.ring = s.ring[1:]
	}
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
			// Too slow: drop the subscriber; it reconnects and resumes or refetches.
			delete(s.subs, ch)
			close(ch)
		}
	}
	if typ == "end" {
		s.ended = true
		for ch := range s.subs {
			delete(s.subs, ch)
			close(ch)
		}
		time.AfterFunc(hubLinger, func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.jobs[job] == s {
				delete(h.jobs, job)
			}
		})
	}
}

// Subscription is a subscriber's view of a job stream.
type Subscription struct {
	Replay   []Event      // events after Last-Event-ID still in the buffer
	Complete bool         // false if events after lastID were dropped: refetch state
	Events   <-chan Event // closed when the job ends or the subscriber is too slow
	Ended    bool         // the job already ended (or is unknown to this process)
	Cancel   func()
}

// Subscribe registers for job events after lastID (0 = only new events).
func (h *Hub) Subscribe(job string, lastID int64) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.jobs[job]
	if !ok {
		return &Subscription{Ended: true, Complete: lastID == 0, Cancel: func() {}}
	}
	sub := &Subscription{Complete: true}
	if lastID > 0 {
		if len(s.ring) > 0 && s.ring[0].ID > lastID+1 {
			sub.Complete = false
		}
		for _, ev := range s.ring {
			if ev.ID > lastID {
				sub.Replay = append(sub.Replay, ev)
			}
		}
	}
	if s.ended {
		sub.Ended = true
		sub.Cancel = func() {}
		return sub
	}
	ch := make(chan Event, subBuffer)
	s.subs[ch] = struct{}{}
	sub.Events = ch
	sub.Cancel = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
	}
	return sub
}
