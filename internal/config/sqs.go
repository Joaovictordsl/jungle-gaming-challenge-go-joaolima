package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type SQS struct {
	Enabled         bool
	Endpoint        string
	Region          string
	QueueName       string
	DLQName         string
	ProviderID      string
	ConsumerName    string
	AccessKeyID     string
	SecretAccessKey string
	Workers         int
}

func LoadSQS() (SQS, error) {
	c := SQS{Endpoint: "http://127.0.0.1:4566", Region: "us-east-1", QueueName: "wager-transactions.fifo", DLQName: "wager-transactions-dlq.fifo", ProviderID: "provider-a", ConsumerName: "wager-transactions-provider-a-v1", Workers: 4}
	if raw, ok := os.LookupEnv("SQS_ENABLED"); ok {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return c, fmt.Errorf("%w: SQS_ENABLED inválido", ErrInvalidConfig)
		}
		c.Enabled = v
	}
	for name, target := range map[string]*string{"SQS_ENDPOINT": &c.Endpoint, "SQS_REGION": &c.Region, "SQS_QUEUE_NAME": &c.QueueName, "SQS_DLQ_NAME": &c.DLQName, "SQS_PROVIDER_ID": &c.ProviderID, "SQS_CONSUMER_NAME": &c.ConsumerName, "SQS_ACCESS_KEY_ID": &c.AccessKeyID, "SQS_SECRET_ACCESS_KEY": &c.SecretAccessKey} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	if raw, ok := os.LookupEnv("SQS_WORKERS"); ok {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return c, fmt.Errorf("%w: SQS_WORKERS inválido", ErrInvalidConfig)
		}
		c.Workers = v
	}
	return c, c.Validate()
}
func (c SQS) Validate() error {
	if !c.Enabled {
		return nil
	}
	for _, value := range []string{c.ProviderID, c.ConsumerName} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return fmt.Errorf("%w: provedor/consumidor SQS inválido", ErrInvalidConfig)
		}
	}
	return validateBroker(c.Endpoint, c.Region, c.AccessKeyID, c.SecretAccessKey, c.QueueName, c.DLQName, c.Workers)
}
func validateBroker(endpoint, region, key, secret, queue, dlq string, workers int) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("%w: endpoint SQS inválido", ErrInvalidConfig)
	}
	for _, value := range []string{region, key, secret} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return fmt.Errorf("%w: configuração SQS incompleta", ErrInvalidConfig)
		}
	}
	for _, name := range []string{queue, dlq} {
		if !strings.HasSuffix(name, ".fifo") || len(name) > 80 || len(name) <= 5 {
			return fmt.Errorf("%w: nome FIFO inválido", ErrInvalidConfig)
		}
		for _, ch := range strings.TrimSuffix(name, ".fifo") {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return fmt.Errorf("%w: nome FIFO inválido", ErrInvalidConfig)
			}
		}
	}
	if queue == dlq || workers < 1 || workers > 16 {
		return fmt.Errorf("%w: filas ou concorrência SQS inválidas", ErrInvalidConfig)
	}
	return nil
}
