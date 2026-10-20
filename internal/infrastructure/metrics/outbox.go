package metrics

import (
	"fmt"
	"sync/atomic"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/application/outbox"
)

type Outbox struct {
	refreshed                                   atomic.Int64
	claimed, recovered, published, failed, lost atomic.Uint64
	pending, lag                                atomic.Int64
}

func (m *Outbox) Claimed(recovered bool) {
	m.claimed.Add(1)
	if recovered {
		m.recovered.Add(1)
	}
}
func (m *Outbox) Published() { m.published.Add(1) }
func (m *Outbox) Failed()    { m.failed.Add(1) }
func (m *Outbox) LeaseLost() { m.lost.Add(1) }
func (m *Outbox) Backlog(b outbox.Backlog) {
	m.pending.Store(b.Pending)
	age := int64(0)
	if b.Oldest != nil {
		age = max(0, int64(time.Since(*b.Oldest)/time.Second))
	}
	m.lag.Store(age)
	m.refreshed.Store(time.Now().Unix())
}
func (m *Outbox) Prometheus() string {
	result := ""
	for _, v := range []struct {
		name  string
		value uint64
	}{{"claimed", m.claimed.Load()}, {"recovered", m.recovered.Load()}, {"published", m.published.Load()}, {"publish_failures", m.failed.Load()}, {"lease_lost", m.lost.Load()}} {
		name := "jungle_outbox_" + v.name + "_total"
		result += fmt.Sprintf("# TYPE %s counter\n%s %d\n", name, name, v.value)
	}
	if m.refreshed.Load() == 0 {
		return result
	}
	for _, v := range []struct {
		name  string
		value int64
	}{{"pending", m.pending.Load()}, {"oldest_pending_age_seconds", m.lag.Load()}, {"backlog_last_refresh_timestamp_seconds", m.refreshed.Load()}} {
		name := "jungle_outbox_" + v.name
		result += fmt.Sprintf("# TYPE %s gauge\n%s %d\n", name, name, v.value)
	}
	return result
}
