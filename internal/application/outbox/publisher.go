package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	appwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
)

const Lease = 30 * time.Second

var ErrLeaseLost = errors.New("reserva da outbox pertence a outra tentativa ou expirou")

type Delivery struct {
	ID, AggregateID, TransactionID, ProviderID, EventType, Token string
	Payload                                                      json.RawMessage
	Sequence                                                     int64
	Attempts                                                     int
	Recovered                                                    bool
}
type Backlog struct {
	Pending int64
	Oldest  *time.Time
}
type Store interface {
	Claim(context.Context, string) (Delivery, bool, error)
	Published(context.Context, Delivery) error
	Retry(context.Context, Delivery, time.Duration) error
	Release(context.Context, Delivery) error
	Backlog(context.Context) (Backlog, error)
}
type Sender interface {
	Send(context.Context, Delivery) error
}
type Observer interface {
	Claimed(bool)
	Published()
	Failed()
	LeaseLost()
	Backlog(Backlog)
}
type Service struct {
	store    Store
	sender   Sender
	ids      appwallet.IDGenerator
	observer Observer
}

func NewService(store Store, sender Sender, ids appwallet.IDGenerator, observer Observer) *Service {
	return &Service{store: store, sender: sender, ids: ids, observer: observer}
}
func RetryDelay(attempt int) time.Duration {
	return min(time.Second*time.Duration(1<<min(max(attempt-1, 0), 6)), time.Minute)
}

// RunOnce reserva em commit curto, envia sem manter conexão/lock SQL e confirma
// com o token daquela tentativa. A publicação tem garantia de pelo menos uma vez.
func (s *Service) RunOnce(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	token, err := s.ids.NewID()
	if err != nil {
		return false, err
	}
	claim, cancel := context.WithTimeout(ctx, 3*time.Second)
	delivery, found, err := s.store.Claim(claim, token)
	cancel()
	if err != nil || !found {
		return false, err
	}
	s.observer.Claimed(delivery.Recovered)
	send, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = s.sender.Send(send, delivery)
	cancel()
	// O encerramento do poller não deve impedir a limpeza/confirmar envio conhecido.
	finish, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err != nil {
		if ctx.Err() != nil {
			return true, errors.Join(err, s.store.Release(finish, delivery))
		}
		s.observer.Failed()
		retryErr := s.store.Retry(finish, delivery, RetryDelay(delivery.Attempts))
		if errors.Is(retryErr, ErrLeaseLost) {
			s.observer.LeaseLost()
		}
		return true, errors.Join(err, retryErr)
	}
	if err = s.store.Published(finish, delivery); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			s.observer.LeaseLost()
		}
		// Envio pode ter chegado. A reentrega conserva eventId e snapshot.
		_ = s.store.Release(finish, delivery)
		return true, err
	}
	s.observer.Published()
	return true, nil
}
func (s *Service) RefreshBacklog(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	v, err := s.store.Backlog(ctx)
	if err == nil {
		s.observer.Backlog(v)
	}
	return err
}
