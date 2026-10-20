package config

import (
	"errors"
	"testing"
)

func TestSQSConfig(t *testing.T) {
	c := SQS{Enabled: true, Endpoint: "http://127.0.0.1:4566", Region: "us-east-1", QueueName: "queue.fifo", DLQName: "dead.fifo", ProviderID: "provider-a", ConsumerName: "consumer", AccessKeyID: "local-key", SecretAccessKey: "local-secret", Workers: 4}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SQS){func(v *SQS) { v.Endpoint = "http://user:secret@localhost:4566" }, func(v *SQS) { v.SecretAccessKey = "" }, func(v *SQS) { v.QueueName = "bad name.fifo" }, func(v *SQS) { v.DLQName = v.QueueName }, func(v *SQS) { v.Workers = 0 }, func(v *SQS) { v.Workers = 17 }, func(v *SQS) { v.ProviderID = "" }, func(v *SQS) { v.ConsumerName = "" }} {
		v := c
		change(&v)
		if !errors.Is(v.Validate(), ErrInvalidConfig) {
			t.Fatal("invalid configuration accepted")
		}
	}
	if err := (SQS{}).Validate(); err != nil {
		t.Fatal("disabled consumer must not require credentials")
	}
	t.Setenv("SQS_ENABLED", "true")
	t.Setenv("SQS_ACCESS_KEY_ID", "local-key")
	t.Setenv("SQS_SECRET_ACCESS_KEY", "local-secret")
	loaded, err := LoadSQS()
	if err != nil || !loaded.Enabled || loaded.ProviderID != "provider-a" || loaded.Workers != 4 {
		t.Fatal("configuration load failed", err)
	}
	t.Setenv("SQS_ENABLED", "perhaps")
	if _, err := LoadSQS(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("invalid boolean accepted")
	}
}
