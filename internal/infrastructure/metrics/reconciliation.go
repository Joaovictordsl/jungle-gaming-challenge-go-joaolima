package metrics

import (
	"fmt"
	"sync/atomic"
)

type Reconciliation struct {
	checks      atomic.Uint64
	divergences atomic.Uint64
}

func NewReconciliation() *Reconciliation { return &Reconciliation{} }
func (m *Reconciliation) ObserveReconciliation(consistent bool) {
	m.checks.Add(1)
	if !consistent {
		m.divergences.Add(1)
	}
}
func (m *Reconciliation) Prometheus() string {
	return fmt.Sprintf("# HELP jungle_reconciliation_checks_total Reconciliações concluídas.\n# TYPE jungle_reconciliation_checks_total counter\njungle_reconciliation_checks_total %d\n# HELP jungle_reconciliation_divergences_total Reconciliações com divergência.\n# TYPE jungle_reconciliation_divergences_total counter\njungle_reconciliation_divergences_total %d\n", m.checks.Load(), m.divergences.Load())
}
