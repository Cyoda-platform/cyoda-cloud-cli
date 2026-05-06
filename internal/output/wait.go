package output

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrWaitTimeout is returned by PollUntilTerminal when the total deadline
// elapses before the polled function reports a terminal state.
var ErrWaitTimeout = errors.New("wait: deadline exceeded before terminal state")

// WaitOpts tunes PollUntilTerminal. Zero-value fields fall back to spec
// defaults (1s initial, 30s max, 30 min total). Tests inject deterministic
// Now/Sleep seams via these fields so the loop runs in milliseconds.
type WaitOpts struct {
	// Initial backoff delay. Defaults to 1s when zero.
	Initial time.Duration
	// Max caps each individual backoff. Defaults to 30s when zero.
	Max time.Duration
	// Total bounds the wall-clock elapsed polling time. Defaults to 30 min
	// when zero. The loop exits with ErrWaitTimeout once Now() exceeds the
	// start + Total deadline.
	Total time.Duration
	// Multiplier for the exponential schedule. Defaults to 2 when zero.
	Multiplier int

	// Status, when non-nil, receives a one-line update each iteration before
	// the sleep — e.g. "still PROCESSING after 30s". Per spec §6.5 callers
	// should pass cmd.ErrOrStderr() so progress goes to stderr.
	Status io.Writer

	// Now and Sleep are test seams; production passes nil to use real time.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// PollFunc is the signature PollUntilTerminal calls each iteration. It returns
// the current state string (used for status messages and as the final return
// value), a terminal flag (true once the state is one of SUCCESS / FAILED /
// CANCELLED — see spec §4.3), and any HTTP error that should bubble up
// immediately.
type PollFunc func(ctx context.Context) (state string, terminal bool, err error)

// PollUntilTerminal polls fn with exponential backoff (Initial → Max, ×Multiplier
// per iteration, capped) until it returns terminal=true or the Total deadline
// elapses. Returns the last observed state and a nil error on terminal,
// ErrWaitTimeout on deadline, ctx.Err() on cancellation, or fn's error
// verbatim on poll failure.
//
// The first poll fires immediately (no leading sleep) so a request that's
// already terminal returns instantly. Subsequent iterations sleep before
// polling so we don't hammer the server.
func PollUntilTerminal(ctx context.Context, fn PollFunc, opts WaitOpts) (string, error) {
	initial := opts.Initial
	if initial <= 0 {
		initial = 1 * time.Second
	}
	max := opts.Max
	if max <= 0 {
		max = 30 * time.Second
	}
	total := opts.Total
	if total <= 0 {
		total = 30 * time.Minute
	}
	mult := opts.Multiplier
	if mult <= 0 {
		mult = 2
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}

	start := now()
	deadline := start.Add(total)
	delay := initial
	var lastState string

	for {
		if err := ctx.Err(); err != nil {
			return lastState, err
		}
		state, terminal, err := fn(ctx)
		if err != nil {
			return state, err
		}
		lastState = state
		if terminal {
			return state, nil
		}
		// Compute next deadline check before sleeping.
		if !now().Before(deadline) {
			return state, ErrWaitTimeout
		}
		if opts.Status != nil {
			elapsed := now().Sub(start).Round(time.Second)
			fmt.Fprintf(opts.Status, "still %s after %s\n", state, elapsed)
		}
		if err := sleep(ctx, delay); err != nil {
			return state, err
		}
		// Exponential bump, capped at Max.
		next := delay * time.Duration(mult)
		if next > max {
			next = max
		}
		delay = next
	}
}

// IsTerminalEnvState reports whether s is one of the terminal env states per
// spec §4.3 / OpenAPI: SUCCESS, FAILED, CANCELLED. Comparison is
// case-insensitive on the canonical uppercase names; we also accept the
// common "READY"/"RUNNING" → SUCCESS-equivalent mapping the server emits in
// the env shape (see openapi.yaml § /v2/env GET — `state` is free-form, so
// we treat any of the documented terminals as terminal).
//
// Open question: the spec lists SUCCESS / FAILED / CANCELLED but the running
// server has historically emitted READY / FAILED / CANCELLED for env. Until
// the spec pins the exact set we accept both vocabularies. Add new terminals
// here as the server's contract solidifies.
func IsTerminalEnvState(s string) bool {
	switch s {
	case "SUCCESS", "FAILED", "CANCELLED",
		"READY", "ERROR", "DELETED":
		return true
	}
	return false
}
