package tts

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RetryOptions configures WithRetry. Zero-valued fields use defaults.
type RetryOptions struct {
	MaxAttempts int                                        // default 5
	BaseDelay   time.Duration                              // default 1s
	MaxDelay    time.Duration                              // default 60s
	Sleep       func(context.Context, time.Duration) error // nil = real sleep honoring ctx
}

// WithRetry wraps s with exponential backoff on retryable errors (server-side
// and rate-limit errors). Non-retryable errors fail immediately.
func WithRetry(s Synthesizer, opts RetryOptions) Synthesizer {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = time.Second
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = 60 * time.Second
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepContext
	}
	return &retrySynthesizer{inner: s, opts: opts}
}

type retrySynthesizer struct {
	inner Synthesizer
	opts  RetryOptions
}

func (r *retrySynthesizer) Synthesize(ctx context.Context, req Request) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < r.opts.MaxAttempts; attempt++ {
		audio, err := r.inner.Synthesize(ctx, req)
		if err == nil {
			return audio, nil
		}
		if !isRetryable(err) {
			return nil, err
		}
		lastErr = err
		if attempt+1 >= r.opts.MaxAttempts {
			break
		}
		if err := r.opts.Sleep(ctx, backoffDelay(r.opts.BaseDelay, r.opts.MaxDelay, attempt)); err != nil {
			return nil, fmt.Errorf("tts retry aborted: %w", err)
		}
	}
	return nil, fmt.Errorf("tts synthesis failed after %d attempts: %w", r.opts.MaxAttempts, lastErr)
}

func (r *retrySynthesizer) Close() error {
	return r.inner.Close()
}

// isRetryable reports whether err is a gRPC error worth retrying: rate-limit
// or server-side failures. Everything else fails immediately.
func isRetryable(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.ResourceExhausted, codes.Unavailable, codes.Internal,
		codes.Aborted, codes.DeadlineExceeded, codes.Unknown:
		return true
	default:
		return false
	}
}

// backoffDelay returns base * 2^attempt, capped at max.
func backoffDelay(base, max time.Duration, attempt int) time.Duration {
	d := base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= max || d <= 0 { // d <= 0 guards against overflow
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
