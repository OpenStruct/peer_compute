// Package retry provides exponential backoff with jitter for retrying operations.
package retry

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Config controls retry behavior.
type Config struct {
	MaxAttempts int           // maximum number of attempts (0 = unlimited, uses context deadline)
	BaseDelay   time.Duration // initial delay between retries
	MaxDelay    time.Duration // cap on delay
	Jitter      float64       // 0.0–1.0, fraction of delay to randomize
}

// DefaultConfig returns a sensible default: 3 attempts, 500ms base, 10s max, 0.3 jitter.
func DefaultConfig() Config {
	return Config{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Jitter:      0.3,
	}
}

// Do executes fn up to cfg.MaxAttempts times, with exponential backoff between failures.
// It stops early if ctx is cancelled. Returns the last error if all attempts fail.
func Do(ctx context.Context, cfg Config, fn func(ctx context.Context) error) error {
	var err error
	for attempt := 0; cfg.MaxAttempts == 0 || attempt < cfg.MaxAttempts; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Don't sleep after the last attempt
		if cfg.MaxAttempts > 0 && attempt >= cfg.MaxAttempts-1 {
			break
		}

		delay := time.Duration(float64(cfg.BaseDelay) * math.Pow(2, float64(attempt)))
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}

		// Add jitter: delay +/- (jitter * delay * random)
		if cfg.Jitter > 0 {
			jitter := time.Duration(float64(delay) * cfg.Jitter * (2*rand.Float64() - 1))
			delay += jitter
			if delay < 0 {
				delay = cfg.BaseDelay
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}
