package streamsession

import (
	"context"
	"errors"
	"time"

	"kogen-go/internal/xspec/protocol"
)

// StreamFactory and SessionFactory are registered by the later xspec
// integration command. Each call returns a fresh replay state and all effects
// stay local to that instance.
func StreamFactory(context.Context) (protocol.Slice, error) {
	return newStreamSlice(), nil
}

func SessionFactory(context.Context) (protocol.Slice, error) {
	return newSessionSlice(), nil
}

type replayClock struct {
	now time.Time
}

func newReplayClock() *replayClock {
	return &replayClock{now: time.Unix(1, 0)}
}

func (c *replayClock) Now() time.Time { return c.now }

func (c *replayClock) Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay < 0 {
		return errors.New("replay clock cannot sleep for a negative duration")
	}
	c.now = c.now.Add(delay)
	return nil
}

// ceilingJitter selects the top of the production retry policy's allowed
// half-to-ceiling range. This makes the model's recorded ceiling reproducible.
type ceilingJitter struct{}

func (ceilingJitter) Uint64n(upperExclusive uint64) (uint64, error) {
	if upperExclusive == 0 {
		return 0, errors.New("replay jitter received an empty range")
	}
	return upperExclusive - 1, nil
}
