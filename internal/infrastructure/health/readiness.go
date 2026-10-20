package health

import (
	"context"
	"time"
)

const Timeout = 2 * time.Second

type Status string

const (
	OK          Status = "ok"
	Unavailable Status = "unavailable"
	Disabled    Status = "disabled"
)

type Checks struct {
	Postgres     Status `json:"postgres"`
	SQSConsumer  Status `json:"sqsConsumer"`
	SQSPublisher Status `json:"sqsPublisher"`
}

func (c Checks) Ready() bool {
	return c.Postgres == OK && (c.SQSConsumer == OK || c.SQSConsumer == Disabled) && (c.SQSPublisher == OK || c.SQSPublisher == Disabled)
}

type Checker interface{ Check(context.Context) Checks }
type Probe func(context.Context) error

type Service struct {
	postgres, consumer, publisher Probe
	timeout                       time.Duration
}

// Probes SQS nil indicam módulos desabilitados. PostgreSQL é sempre obrigatório.
func NewService(postgres, consumer, publisher Probe) *Service {
	return &Service{postgres: postgres, consumer: consumer, publisher: publisher, timeout: Timeout}
}
func (s *Service) Check(ctx context.Context) Checks {
	result := Checks{Postgres: Unavailable, SQSConsumer: Disabled, SQSPublisher: Disabled}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	type outcome struct {
		index  int
		status Status
	}
	outcomes := make(chan outcome, 3)

	probes := []Probe{s.postgres, s.consumer, s.publisher}
	if s.consumer != nil {
		result.SQSConsumer = Unavailable
	}
	if s.publisher != nil {
		result.SQSPublisher = Unavailable
	}
	if ctx.Err() != nil {
		return result
	}
	pending := 0
	for index, probe := range probes {
		if probe == nil {
			continue
		}
		pending++
		go func() {
			status := OK
			if probe(ctx) != nil {
				status = Unavailable
			}
			outcomes <- outcome{index, status}
		}()
	}
	for pending > 0 {
		select {
		case done := <-outcomes:
			switch done.index {
			case 0:
				result.Postgres = done.status
			case 1:
				result.SQSConsumer = done.status
			case 2:
				result.SQSPublisher = done.status
			}
			pending--
		case <-ctx.Done():
			return result
		}
	}
	return result
}
