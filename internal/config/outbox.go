package config

import (
	"fmt"
	"os"
	"strconv"
)

type Outbox struct {
	Enabled                                                            bool
	Endpoint, Region, QueueName, DLQName, AccessKeyID, SecretAccessKey string
	Workers                                                            int
}

func LoadOutbox() (Outbox, error) {
	c := Outbox{Endpoint: "http://127.0.0.1:4566", Region: "us-east-1", QueueName: "wallet-events.fifo", DLQName: "wallet-events-dlq.fifo", Workers: 2}
	if raw, ok := os.LookupEnv("OUTBOX_ENABLED"); ok {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return c, fmt.Errorf("%w: OUTBOX_ENABLED inválido", ErrInvalidConfig)
		}
		c.Enabled = v
	}
	for name, target := range map[string]*string{"OUTBOX_ENDPOINT": &c.Endpoint, "OUTBOX_REGION": &c.Region, "OUTBOX_QUEUE_NAME": &c.QueueName, "OUTBOX_DLQ_NAME": &c.DLQName, "OUTBOX_ACCESS_KEY_ID": &c.AccessKeyID, "OUTBOX_SECRET_ACCESS_KEY": &c.SecretAccessKey} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	if raw, ok := os.LookupEnv("OUTBOX_WORKERS"); ok {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return c, fmt.Errorf("%w: OUTBOX_WORKERS inválido", ErrInvalidConfig)
		}
		c.Workers = v
	}
	return c, c.Validate()
}
func (c Outbox) Validate() error {
	if !c.Enabled {
		return nil
	}
	return validateBroker(c.Endpoint, c.Region, c.AccessKeyID, c.SecretAccessKey, c.QueueName, c.DLQName, c.Workers)
}
