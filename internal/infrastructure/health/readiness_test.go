package health

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadinessDependencies(t *testing.T) {
	ok := func(context.Context) error { return nil }
	failed := func(context.Context) error { return errors.New("private endpoint and credentials") }
	for _, tc := range []struct {
		name        string
		pg, in, out Probe
		want        Checks
		ready       bool
	}{
		{"all enabled", ok, ok, ok, Checks{OK, OK, OK}, true},
		{"HTTP only", ok, nil, nil, Checks{OK, Disabled, Disabled}, true},
		{"postgres required", nil, ok, ok, Checks{Unavailable, OK, OK}, false},
		{"postgres down", failed, ok, ok, Checks{Unavailable, OK, OK}, false},
		{"consumer down", ok, failed, ok, Checks{OK, Unavailable, OK}, false},
		{"publisher down", ok, ok, failed, Checks{OK, OK, Unavailable}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewService(tc.pg, tc.in, tc.out).Check(context.Background())
			if got != tc.want || got.Ready() != tc.ready {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
func TestReadinessProbesRunInParallelWithOneDeadline(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var completed atomic.Int32
	blocked := func(ctx context.Context) error {
		started <- struct{}{}
		defer completed.Add(1)
		<-ctx.Done()
		<-release
		return ctx.Err()
	}
	service := NewService(blocked, blocked, blocked)
	service.timeout = 100 * time.Millisecond
	done := make(chan Checks, 1)
	go func() { done <- service.Check(context.Background()) }()
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("probes não iniciaram em paralelo")
		}
	}
	// Mesmo um probe que demore a respeitar cancelamento não prende o handler.
	select {
	case got := <-done:
		if got.Ready() || got != (Checks{Unavailable, Unavailable, Unavailable}) {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("prazo global ignorado")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for completed.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if completed.Load() != 3 {
		t.Fatal("probes não foram liberados")
	}
}
func TestReadinessPropagatesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	probe := func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }
	done := make(chan Checks, 1)
	go func() { done <- NewService(probe, nil, nil).Check(ctx) }()
	<-started
	cancel()
	select {
	case got := <-done:
		if got.Postgres != Unavailable {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelamento não respeitado")
	}
}
