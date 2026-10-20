package metrics

import (
	"fmt"
	"sync/atomic"
)

type Messaging struct {
	received, completed, replayed, retried, failed, deleteFailed atomic.Uint64
	Outbox                                                       Outbox
	Financial                                                    Financial
}

func NewMessaging() *Messaging { return &Messaging{} }
func (m *Messaging) Received() { m.received.Add(1) }
func (m *Messaging) Completed(replay bool) {
	m.completed.Add(1)
	if replay {
		m.replayed.Add(1)
	}
}
func (m *Messaging) Retry()        { m.retried.Add(1) }
func (m *Messaging) Failed()       { m.failed.Add(1) }
func (m *Messaging) DeleteFailed() { m.deleteFailed.Add(1) }
func (m *Messaging) Prometheus() string {
	values := []struct {
		name  string
		value uint64
	}{{"received", m.received.Load()}, {"completed", m.completed.Load()}, {"inbox_replayed", m.replayed.Load()}, {"retried", m.retried.Load()}, {"failed_delivery", m.failed.Load()}, {"delete_failed", m.deleteFailed.Load()}}
	result := ""
	for _, v := range values {
		name := "jungle_sqs_" + v.name + "_total"
		result += fmt.Sprintf("# TYPE %s counter\n%s %d\n", name, name, v.value)
	}
	return result + m.Outbox.Prometheus() + m.Financial.Prometheus()
}
