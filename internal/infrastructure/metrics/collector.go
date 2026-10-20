package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Probe func(context.Context) (string, error)
type Collector struct{ postgres, consumer, publisher Probe }

func NewCollector(postgres, consumer, publisher Probe) *Collector {
	return &Collector{postgres: postgres, consumer: consumer, publisher: publisher}
}

// Coleta metadados, nunca recebe mensagens. Cada scrape tem um prazo global;
// uma fonte indisponível não apaga os contadores nem informa amostras falsas.
func (c *Collector) Prometheus(ctx context.Context) string {
	if c == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	type result struct {
		index int
		text  string
		err   error
	}
	sources := [...]struct {
		name  string
		probe Probe
	}{{"postgres", c.postgres}, {"sqs_consumer", c.consumer}, {"sqs_publisher", c.publisher}}
	results := make(chan result, len(sources))
	pending := 0
	texts := [len(sources)]string{}
	up := [len(sources)]bool{}
	for i, source := range sources {
		if source.probe == nil {
			continue
		}
		pending++
		go func() { value, err := source.probe(ctx); results <- result{i, value, err} }()
	}
	for pending > 0 {
		select {
		case r := <-results:
			pending--
			up[r.index] = r.err == nil
			if r.err == nil {
				texts[r.index] = r.text
			}
		case <-ctx.Done():
			pending = 0
		}
	}
	var out strings.Builder
	out.WriteString("# TYPE jungle_metrics_source_up gauge\n")
	out.WriteString("# TYPE jungle_sqs_dlq_messages gauge\n")
	for i, source := range sources {
		if source.probe == nil {
			continue
		}
		value := 0
		if up[i] {
			value = 1
		}
		fmt.Fprintf(&out, "jungle_metrics_source_up{source=%q} %d\n", source.name, value)
		out.WriteString(texts[i])
	}
	return out.String()
}
