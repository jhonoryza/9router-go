package fetchgate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// An idle gate must not delay the single caller: the dashboard's per-provider
// refresh button pays nothing, only the burst behind it is paced.
func TestGateAcquire_IdleGateReturnsImmediately(t *testing.T) {
	g := New(250*time.Millisecond, 120*time.Millisecond)

	start := time.Now()
	if err := g.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("idle gate delayed the first caller by %s", elapsed)
	}
}

// The whole point of the gate: N simultaneous callers must not start in the
// same millisecond. This is the ten-accounts-on-one-IP case from issue #30.
//
// The gap is judged on the instants the gate granted, not on when each caller
// goroutine was scheduled after Acquire returned. A caller's return time also
// carries OS scheduler delay, which on a loaded -race run overshoots the grant
// by tens of milliseconds and reports a pacing failure the gate never
// committed.
func TestGateAcquire_SpacesConcurrentCallers(t *testing.T) {
	const (
		callers = 8
		minGap  = 40 * time.Millisecond
	)
	g := New(minGap, 0)

	var (
		mu      sync.Mutex
		granted []time.Time
	)
	g.onGrant = func(start time.Time) {
		mu.Lock()
		granted = append(granted, start)
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Acquire(t.Context()); err != nil {
				t.Errorf("Acquire: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	seq := append([]time.Time(nil), granted...)
	mu.Unlock()

	if len(seq) != callers {
		t.Fatalf("got %d slots, want %d", len(seq), callers)
	}
	// onGrant runs inside reserve() in reservation order, so seq is already the
	// order the gate handed the slots out.
	for i := 1; i < len(seq); i++ {
		if gap := seq[i].Sub(seq[i-1]); gap < minGap {
			t.Errorf("caller %d was granted %s after the previous one, want >= %s", i, gap, minGap)
		}
	}
}

// Jitter must only ever add delay: the floor stays the hard guarantee.
//
// Judged on granted instants, same as the spacing test above, and with a gap
// wide enough to stay clear of scheduler noise. A caller-side stopwatch here
// has no margin at all: jitter is drawn from [0, maxJitter], so a 30ms floor
// can legitimately be followed by a zero-jitter slot and the next grant is due
// exactly 30ms out, leaving nothing to absorb a late goroutine wake-up.
func TestGateAcquire_JitterOnlyWidensTheGap(t *testing.T) {
	const (
		minGap = 40 * time.Millisecond
		jitter = 60 * time.Millisecond
	)
	g := New(minGap, jitter)

	var (
		mu      sync.Mutex
		granted []time.Time
	)
	g.onGrant = func(start time.Time) {
		mu.Lock()
		granted = append(granted, start)
		mu.Unlock()
	}

	const slots = 6
	for range slots {
		if err := g.Acquire(t.Context()); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}

	mu.Lock()
	seq := append([]time.Time(nil), granted...)
	mu.Unlock()

	if len(seq) != slots {
		t.Fatalf("got %d slots, want %d", len(seq), slots)
	}
	for i := 1; i < len(seq); i++ {
		if gap := seq[i].Sub(seq[i-1]); gap < minGap {
			t.Errorf("slot %d was granted %s after the previous one, want >= %s", i, gap, minGap)
		}
	}
}

// A dashboard that navigates away mid-wait must not hold a slot against the
// requests behind it, and it must see its own cancellation as an error.
func TestGateAcquire_CanceledContextReleasesItsWait(t *testing.T) {
	g := New(200*time.Millisecond, 0)

	if err := g.Acquire(t.Context()); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := g.Acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire error = %v, want context.Canceled", err)
	}
}

// A zero gap is a legitimate configuration (pure jitter, no floor), and must
// not be mistaken for a misconfigured gate.
func TestGateAcquire_ZeroGapStillGates(t *testing.T) {
	g := New(0, 0)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Acquire(t.Context()); err != nil {
				t.Errorf("Acquire: %v", err)
			}
		}()
	}
	wg.Wait()
}
