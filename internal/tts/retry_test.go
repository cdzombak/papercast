package tts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeSynthesizer returns errs in order, then audio for all further calls.
type fakeSynthesizer struct {
	errs  []error
	audio []byte
	calls int
}

func (f *fakeSynthesizer) Synthesize(_ context.Context, _ Request) ([]byte, error) {
	f.calls++
	if f.calls <= len(f.errs) {
		return nil, f.errs[f.calls-1]
	}
	return f.audio, nil
}

func (f *fakeSynthesizer) Close() error { return nil }

func collectSleep(delays *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		*delays = append(*delays, d)
		return nil
	}
}

func TestWithRetry_FailThenSucceed(t *testing.T) {
	rateLimited := status.Error(codes.ResourceExhausted, "quota exceeded")
	fake := &fakeSynthesizer{
		errs:  []error{rateLimited, rateLimited, rateLimited},
		audio: []byte("mp3-bytes"),
	}
	var delays []time.Duration
	s := WithRetry(fake, RetryOptions{
		MaxAttempts: 5,
		BaseDelay:   time.Second,
		MaxDelay:    60 * time.Second,
		Sleep:       collectSleep(&delays),
	})

	audio, err := s.Synthesize(context.Background(), Request{Payload: "hi"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(audio) != "mp3-bytes" {
		t.Errorf("audio = %q, want %q", audio, "mp3-bytes")
	}
	if fake.calls != 4 {
		t.Errorf("calls = %d, want 4", fake.calls)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Errorf("delays[%d] = %v, want %v", i, delays[i], want[i])
		}
	}
}

func TestWithRetry_DelayCappedAtMaxDelay(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "backend down")
	fake := &fakeSynthesizer{
		errs:  []error{unavailable, unavailable, unavailable, unavailable, unavailable},
		audio: []byte("ok"),
	}
	var delays []time.Duration
	s := WithRetry(fake, RetryOptions{
		MaxAttempts: 6,
		BaseDelay:   time.Second,
		MaxDelay:    4 * time.Second,
		Sleep:       collectSleep(&delays),
	})

	if _, err := s.Synthesize(context.Background(), Request{}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Errorf("delays[%d] = %v, want %v", i, delays[i], want[i])
		}
	}
}

func TestWithRetry_RetryableCodes(t *testing.T) {
	for _, code := range []codes.Code{codes.ResourceExhausted, codes.Unavailable, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			fake := &fakeSynthesizer{
				errs:  []error{status.Error(code, "transient")},
				audio: []byte("ok"),
			}
			var delays []time.Duration
			s := WithRetry(fake, RetryOptions{Sleep: collectSleep(&delays)})
			if _, err := s.Synthesize(context.Background(), Request{}); err != nil {
				t.Fatalf("Synthesize: %v", err)
			}
			if fake.calls != 2 {
				t.Errorf("calls = %d, want 2", fake.calls)
			}
		})
	}
}

func TestWithRetry_NonRetryableFailsImmediately(t *testing.T) {
	badArg := status.Error(codes.InvalidArgument, "bad SSML")
	fake := &fakeSynthesizer{errs: []error{badArg}}
	var delays []time.Duration
	s := WithRetry(fake, RetryOptions{Sleep: collectSleep(&delays)})

	_, err := s.Synthesize(context.Background(), Request{})
	if !errors.Is(err, badArg) {
		t.Fatalf("err = %v, want %v", err, badArg)
	}
	if fake.calls != 1 {
		t.Errorf("calls = %d, want 1", fake.calls)
	}
	if len(delays) != 0 {
		t.Errorf("delays = %v, want none", delays)
	}
}

func TestWithRetry_NonGRPCErrorFailsImmediately(t *testing.T) {
	plainErr := errors.New("not a grpc error")
	fake := &fakeSynthesizer{errs: []error{plainErr}}
	s := WithRetry(fake, RetryOptions{Sleep: collectSleep(&[]time.Duration{})})

	_, err := s.Synthesize(context.Background(), Request{})
	if !errors.Is(err, plainErr) {
		t.Fatalf("err = %v, want %v", err, plainErr)
	}
	if fake.calls != 1 {
		t.Errorf("calls = %d, want 1", fake.calls)
	}
}

func TestWithRetry_ExhaustedAttempts(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "still down")
	fake := &fakeSynthesizer{
		errs: []error{unavailable, unavailable, unavailable},
	}
	var delays []time.Duration
	s := WithRetry(fake, RetryOptions{
		MaxAttempts: 3,
		Sleep:       collectSleep(&delays),
	})

	_, err := s.Synthesize(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("err = %v, want mention of 3 attempts", err)
	}
	if !errors.Is(err, unavailable) {
		t.Errorf("err = %v, want wrapped %v", err, unavailable)
	}
	if fake.calls != 3 {
		t.Errorf("calls = %d, want 3", fake.calls)
	}
}

func TestWithRetry_SleepCancellationAborts(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "down")
	fake := &fakeSynthesizer{
		errs: []error{unavailable, unavailable, unavailable, unavailable},
	}
	s := WithRetry(fake, RetryOptions{
		MaxAttempts: 5,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			return context.Canceled
		},
	})

	_, err := s.Synthesize(context.Background(), Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if fake.calls != 1 {
		t.Errorf("calls = %d, want 1", fake.calls)
	}
}
