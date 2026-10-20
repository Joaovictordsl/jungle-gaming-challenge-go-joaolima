package outbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

type ids struct{}

func (ids) NewID() (string, error) { return "token", nil }

type storeProbe struct {
	published, retried, released bool
	publishErr                   error
	delay                        time.Duration
}

func (p *storeProbe) Claim(context.Context, string) (Delivery, bool, error) {
	return Delivery{ID: "event", Token: "token", Attempts: 3}, true, nil
}
func (p *storeProbe) Published(context.Context, Delivery) error {
	p.published = true
	return p.publishErr
}
func (p *storeProbe) Retry(_ context.Context, _ Delivery, d time.Duration) error {
	p.retried = true
	p.delay = d
	return nil
}
func (p *storeProbe) Release(context.Context, Delivery) error  { p.released = true; return nil }
func (p *storeProbe) Backlog(context.Context) (Backlog, error) { return Backlog{}, nil }

type senderProbe struct {
	err    error
	cancel context.CancelFunc
}

func (p senderProbe) Send(ctx context.Context, _ Delivery) error {
	if p.cancel != nil {
		p.cancel()
		return ctx.Err()
	}
	return p.err
}

type observerProbe struct{ published, failed, lost int }

func (p *observerProbe) Claimed(bool)    {}
func (p *observerProbe) Published()      { p.published++ }
func (p *observerProbe) Failed()         { p.failed++ }
func (p *observerProbe) LeaseLost()      { p.lost++ }
func (p *observerProbe) Backlog(Backlog) {}
func TestPublisherConfirmsOnlyAfterSend(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		sendErr, publishErr       error
		cancel                    bool
		published, retry, release bool
	}{{name: "success", published: true}, {name: "send failed", sendErr: errors.New("network"), retry: true}, {name: "commit failed after send", publishErr: errors.New("database"), published: true, release: true}, {name: "stale lease", publishErr: ErrLeaseLost, published: true, release: true}, {name: "shutdown", cancel: true, release: true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &storeProbe{publishErr: tc.publishErr}
			sender := senderProbe{err: tc.sendErr}
			if tc.cancel {
				sender.cancel = cancel
			}
			observer := &observerProbe{}
			service := NewService(p, sender, ids{}, observer)
			worked, err := service.RunOnce(ctx)
			if !worked || (err == nil) != (tc.sendErr == nil && tc.publishErr == nil && !tc.cancel) || p.published != tc.published || p.retried != tc.retry || p.released != tc.release {
				t.Fatalf("sequência incorreta: %+v erro=%v", p, err)
			}
			if tc.retry && p.delay != 4*time.Second {
				t.Fatal("backoff incorreto")
			}
			if tc.publishErr == ErrLeaseLost && observer.lost != 1 {
				t.Fatal("lease perdida não observada")
			}
			if observer.published > 0 && (tc.sendErr != nil || tc.publishErr != nil || tc.cancel) {
				t.Fatal("publicação indevida")
			}
		})
	}
}
func TestPublisherBackoffBounded(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{{0, time.Second}, {1, time.Second}, {2, 2 * time.Second}, {6, 32 * time.Second}, {7, time.Minute}, {1000000, time.Minute}} {
		if got := RetryDelay(tc.attempt); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
