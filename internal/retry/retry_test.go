package retry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", cfg.MaxAttempts)
	}
	if cfg.BaseDelay != 500*time.Millisecond {
		t.Errorf("BaseDelay = %v, want 500ms", cfg.BaseDelay)
	}
	if cfg.MaxDelay != 10*time.Second {
		t.Errorf("MaxDelay = %v, want 10s", cfg.MaxDelay)
	}
	if cfg.Jitter != 0.3 {
		t.Errorf("Jitter = %f, want 0.3", cfg.Jitter)
	}
}

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	cfg := Config{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	attempts := 0
	err := Do(context.Background(), cfg, func(_ context.Context) error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

func TestDo_SuccessOnRetry(t *testing.T) {
	cfg := Config{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	attempts := 0
	err := Do(context.Background(), cfg, func(_ context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("fail")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestDo_AllAttemptsFail(t *testing.T) {
	cfg := Config{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	attempts := 0
	err := Do(context.Background(), cfg, func(_ context.Context) error {
		attempts++
		return fmt.Errorf("fail %d", attempts)
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestDo_ContextCancelled(t *testing.T) {
	cfg := Config{MaxAttempts: 10, BaseDelay: 50 * time.Millisecond, MaxDelay: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := Do(ctx, cfg, func(_ context.Context) error {
		attempts++
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts >= 10 {
		t.Errorf("expected fewer than 10 attempts due to cancellation, got %d", attempts)
	}
}

func TestDo_MaxDelayRespected(t *testing.T) {
	cfg := Config{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, Jitter: 0}
	attempts := 0
	start := time.Now()
	_ = Do(context.Background(), cfg, func(_ context.Context) error {
		attempts++
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	if attempts != 5 {
		t.Errorf("attempts = %d, want 5", attempts)
	}
	// With MaxDelay=2ms and 4 sleeps (no sleep after last), total should be well under 100ms
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed %v exceeded expected bound", elapsed)
	}
}

func TestDo_ZeroJitter(t *testing.T) {
	cfg := Config{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: 0}
	attempts := 0
	err := Do(context.Background(), cfg, func(_ context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("fail")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}
