package output

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeClock advances on every Sleep call by the requested duration.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func TestPollUntilTerminal_TerminalOnFirstCall(t *testing.T) {
	clk := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	state, err := PollUntilTerminal(context.Background(),
		func(ctx context.Context) (string, bool, error) {
			calls++
			return "SUCCESS", true, nil
		},
		WaitOpts{Now: clk.Now, Sleep: clk.Sleep},
	)
	if err != nil {
		t.Fatalf("PollUntilTerminal: %v", err)
	}
	if state != "SUCCESS" {
		t.Errorf("state = %q, want SUCCESS", state)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if len(clk.sleeps) != 0 {
		t.Errorf("expected zero sleeps before terminal, got %v", clk.sleeps)
	}
}

func TestPollUntilTerminal_ExponentialBackoffSequence(t *testing.T) {
	clk := &fakeClock{now: time.Unix(0, 0)}
	// Drive 7 non-terminal polls then SUCCESS so we observe the full
	// 1,2,4,8,16,30,30 capped sequence.
	calls := 0
	_, err := PollUntilTerminal(context.Background(),
		func(ctx context.Context) (string, bool, error) {
			calls++
			if calls > 7 {
				return "SUCCESS", true, nil
			}
			return "PROCESSING", false, nil
		},
		WaitOpts{Now: clk.Now, Sleep: clk.Sleep},
	)
	if err != nil {
		t.Fatalf("PollUntilTerminal: %v", err)
	}
	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // capped from 32
		30 * time.Second,
	}
	if len(clk.sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", clk.sleeps, want)
	}
	for i, d := range want {
		if clk.sleeps[i] != d {
			t.Errorf("sleep[%d] = %v, want %v", i, clk.sleeps[i], d)
		}
	}
}

func TestPollUntilTerminal_DeadlineExceeded(t *testing.T) {
	clk := &fakeClock{now: time.Unix(0, 0)}
	// Total = 5s. With Initial=1s, Mult=2: schedule is 1+2+...; after a
	// couple of sleeps the deadline trips.
	_, err := PollUntilTerminal(context.Background(),
		func(ctx context.Context) (string, bool, error) {
			return "PROCESSING", false, nil
		},
		WaitOpts{
			Initial: 1 * time.Second,
			Max:     30 * time.Second,
			Total:   5 * time.Second,
			Now:     clk.Now,
			Sleep:   clk.Sleep,
		},
	)
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want ErrWaitTimeout", err)
	}
}

func TestPollUntilTerminal_ContextCancel(t *testing.T) {
	clk := &fakeClock{now: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := PollUntilTerminal(ctx,
		func(ctx context.Context) (string, bool, error) {
			calls++
			if calls == 2 {
				cancel()
			}
			return "PROCESSING", false, nil
		},
		WaitOpts{Now: clk.Now, Sleep: clk.Sleep},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestPollUntilTerminal_PollErrorPropagates(t *testing.T) {
	clk := &fakeClock{now: time.Unix(0, 0)}
	want := errors.New("network kapow")
	_, err := PollUntilTerminal(context.Background(),
		func(ctx context.Context) (string, bool, error) {
			return "", false, want
		},
		WaitOpts{Now: clk.Now, Sleep: clk.Sleep},
	)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestPollUntilTerminal_StatusMessages(t *testing.T) {
	clk := &fakeClock{now: time.Unix(0, 0)}
	var status bytes.Buffer
	calls := 0
	_, err := PollUntilTerminal(context.Background(),
		func(ctx context.Context) (string, bool, error) {
			calls++
			if calls > 2 {
				return "SUCCESS", true, nil
			}
			return "PROCESSING", false, nil
		},
		WaitOpts{Now: clk.Now, Sleep: clk.Sleep, Status: &status},
	)
	if err != nil {
		t.Fatalf("PollUntilTerminal: %v", err)
	}
	out := status.String()
	if !strings.Contains(out, "still PROCESSING after") {
		t.Errorf("status missing message:\n%s", out)
	}
}

// TestIsTerminalState exercises the shared terminal-state predicate.
//
// Two vocabularies are accepted (see IsTerminalState's doc):
//
//   - Legacy upper-case (SUCCESS/FAILED/CANCELLED) used by build/deploy
//     entities and the v0 single-env API.
//   - TitleCase env-workflow names (Ready, Mint_Failed, Bootstrap_Failed,
//     Job_Failed, Job_Cancelled, Env_Torn_Down) used by /v2/envs*.
//
// Intermediate states (Queued, Job_Scheduled, PROCESSING) and
// case-mismatch variants are non-terminal.
func TestIsTerminalState(t *testing.T) {
	terminals := []string{
		// Legacy.
		"SUCCESS", "FAILED", "CANCELLED",
		// New env-workflow vocabulary.
		"Ready", "Mint_Failed", "Bootstrap_Failed",
		"Job_Failed", "Job_Cancelled", "Env_Torn_Down",
	}
	for _, s := range terminals {
		if !IsTerminalState(s) {
			t.Errorf("IsTerminalState(%q) = false, want true", s)
		}
	}
	nonTerminals := []string{
		"", "PROCESSING", "PENDING", "QUEUED",
		"Queued", "Job_Scheduled", "Bootstrap_Pending",
		// Case-mismatch variants must NOT match.
		"success", "failed", "ready", "READY",
		"env_torn_down", "ENV_TORN_DOWN",
	}
	for _, s := range nonTerminals {
		if IsTerminalState(s) {
			t.Errorf("IsTerminalState(%q) = true, want false", s)
		}
	}
}
