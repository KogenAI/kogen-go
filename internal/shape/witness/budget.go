package witness

import (
	"errors"
	"sync"
)

// Budget serializes output-token reservations across the witness Build,
// adjudicator, and repairs. Concurrent hard R1/R2 requests share this budget.
// Provider adapters reserve the request's maximum before dispatch and complete
// it with the observed output usage afterward.
type Budget struct {
	mu       sync.Mutex
	maximum  int64
	used     int64
	reserved int64
	unknown  bool
}

// OutputReservation accounts for one provider response. A reservation must
// be completed exactly once, including when the response usage is unknown.
type OutputReservation struct {
	budget *Budget
	limit  int64
	done   bool
}

func NewBudget(maximum int64) *Budget {
	return &Budget{maximum: maximum}
}

func (b *Budget) ReserveOutput(maximum int64) (*OutputReservation, error) {
	if b == nil || maximum <= 0 {
		return nil, ErrOutputReservation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unknown {
		return nil, ErrOutputUsageUnknown
	}
	remaining := b.maximum - b.used - b.reserved
	if remaining < 0 || maximum > remaining {
		return nil, ErrOutputBudgetExceeded
	}
	b.reserved += maximum
	return &OutputReservation{budget: b, limit: maximum}, nil
}

func (r *OutputReservation) Complete(actual *int64) error {
	if r == nil || r.budget == nil {
		return ErrOutputReservation
	}
	b := r.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.done || r.limit <= 0 || b.reserved < r.limit {
		return ErrOutputReservation
	}
	r.done = true
	b.reserved -= r.limit
	if actual == nil {
		b.unknown = true
		return ErrOutputUsageUnknown
	}
	if *actual < 0 {
		b.unknown = true
		return ErrOutputReservation
	}
	if *actual > r.limit {
		b.used += *actual
		return ErrOutputBudgetExceeded
	}
	b.used += *actual
	if b.used > b.maximum {
		return ErrOutputBudgetExceeded
	}
	return nil
}

// RemainingOutputTokens returns the amount that can still be reserved. It
// returns zero after any response with unknown usage so no later model request
// can escape the aggregate cap.
func (b *Budget) RemainingOutputTokens() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unknown {
		return 0
	}
	remaining := b.maximum - b.used - b.reserved
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (b *Budget) Status() error {
	if b == nil {
		return ErrOutputReservation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unknown {
		return ErrOutputUsageUnknown
	}
	if b.used > b.maximum {
		return ErrOutputBudgetExceeded
	}
	if b.reserved != 0 {
		return ErrOutputReservation
	}
	return nil
}

// Finish ensures all dispatched requests have supplied a bounded usage result
// before the witness can be published.
func (b *Budget) Finish() error {
	if b == nil {
		return ErrOutputReservation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unknown {
		return ErrOutputUsageUnknown
	}
	if b.used > b.maximum {
		return ErrOutputBudgetExceeded
	}
	if b.reserved != 0 {
		return errors.Join(ErrOutputReservation, errors.New("provider response has not completed"))
	}
	return nil
}
