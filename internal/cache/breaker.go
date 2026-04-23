package cache

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

const (
	breakerStateClosed int32 = iota
	breakerStateOpen
	breakerStateHalfOpen
)

type breakerBucket struct {
	ts       int64
	attempts int32
	failures int32
}

// CircuitBreaker tracks Redis command-path health using an error-rate window.
type CircuitBreaker struct {
	state    atomic.Int32
	openedAt atomic.Int64

	window     time.Duration
	minSamples int32
	errorRate  float64
	cooldown   time.Duration

	logger  zerolog.Logger
	mu      sync.Mutex
	buckets []breakerBucket
}

func NewCircuitBreaker(logger zerolog.Logger) *CircuitBreaker {
	window := 10 * time.Second
	return &CircuitBreaker{
		window:     window,
		minSamples: 20,
		errorRate:  0.5,
		cooldown:   10 * time.Second,
		logger:     logger,
		buckets:    make([]breakerBucket, int(window.Seconds())),
	}
}

func (cb *CircuitBreaker) Allow() bool {
	switch cb.state.Load() {
	case breakerStateClosed:
		return true
	case breakerStateOpen:
		if time.Since(time.Unix(0, cb.openedAt.Load())) > cb.cooldown {
			if cb.state.CompareAndSwap(breakerStateOpen, breakerStateHalfOpen) {
				cb.logger.Error().Msg("redis circuit breaker half-open")
				return true
			}
		}
		return false
	case breakerStateHalfOpen:
		return false
	default:
		return false
	}
}

func (cb *CircuitBreaker) Ready() bool {
	return cb != nil && cb.state.Load() == breakerStateClosed
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.record(false)
	if cb.state.CompareAndSwap(breakerStateHalfOpen, breakerStateClosed) {
		cb.logger.Error().Msg("redis circuit breaker closed")
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.record(true)
	if cb.state.Load() == breakerStateHalfOpen {
		cb.open()
		return
	}

	attempts, failures := cb.windowStats()
	if attempts >= cb.minSamples && float64(failures)/float64(attempts) >= cb.errorRate {
		cb.open()
	}
}

func (cb *CircuitBreaker) ForceOpen() {
	cb.open()
}

func (cb *CircuitBreaker) State() int32 {
	return cb.state.Load()
}

func (cb *CircuitBreaker) open() {
	cb.openedAt.Store(time.Now().UnixNano())
	if cb.state.Swap(breakerStateOpen) != breakerStateOpen {
		cb.logger.Error().Msg("redis circuit breaker open")
	}
}

func (cb *CircuitBreaker) record(failed bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now().Unix()
	index := int(now % int64(len(cb.buckets)))
	b := &cb.buckets[index]
	if b.ts != now {
		b.ts = now
		b.attempts = 0
		b.failures = 0
	}
	b.attempts++
	if failed {
		b.failures++
	}
}

func (cb *CircuitBreaker) windowStats() (int32, int32) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	var attempts int32
	var failures int32
	cutoff := time.Now().Add(-cb.window).Unix()
	for i := range cb.buckets {
		b := cb.buckets[i]
		if b.ts < cutoff {
			continue
		}
		attempts += b.attempts
		failures += b.failures
	}
	return attempts, failures
}
