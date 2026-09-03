package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
)

// Shipper continuously tails newly committed events and writes them to a
// Sink — the litestream-like mechanism (ADR-001 D7). It polls
// event.Store.After at a fixed interval rather than using a change
// notification (modernc.org/sqlite has none to hook).
type Shipper struct {
	events   *event.Store
	sink     Sink
	interval time.Duration
	lastSeq  int64 // events with Seq <= lastSeq have already been shipped
}

// NewShipper builds a Shipper starting from lastSeq (0 to ship everything;
// pass a snapshot's seq to resume shipping only what's new since it).
func NewShipper(events *event.Store, sink Sink, interval time.Duration, lastSeq int64) *Shipper {
	return &Shipper{events: events, sink: sink, interval: interval, lastSeq: lastSeq}
}

// LastSeq reports the highest seq shipped so far.
func (s *Shipper) LastSeq() int64 { return s.lastSeq }

// Run ships new events until ctx is cancelled, then returns nil. Intended
// to run in its own goroutine for the life of the server process.
func (s *Shipper) Run(ctx context.Context) error {
	if err := s.shipOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.shipOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Shipper) shipOnce(ctx context.Context) error {
	events, err := s.events.After(ctx, s.lastSeq)
	if err != nil {
		return fmt.Errorf("backup: shipper: read new events: %w", err)
	}
	if len(events) == 0 {
		return nil
	}
	if err := s.sink.WriteEvents(ctx, events); err != nil {
		return fmt.Errorf("backup: shipper: write to sink: %w", err)
	}
	s.lastSeq = events[len(events)-1].Seq
	return nil
}
