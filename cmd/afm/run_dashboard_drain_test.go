package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForDashboardDrain_NoClients_ReturnsAfterMinGrace(t *testing.T) {
	start := time.Now()
	waitForDashboardDrainWithTiming(context.Background(), func() int { return 0 }, 30*time.Millisecond, time.Second, 5*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 30*time.Millisecond {
		t.Fatalf("returned too early: %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("returned too late for zero clients: %v", elapsed)
	}
}

func TestWaitForDashboardDrain_WaitsWhileClientConnected_ThenReturnsOnDisconnect(t *testing.T) {
	var connected atomic.Bool
	connected.Store(true)

	go func() {
		time.Sleep(60 * time.Millisecond)
		connected.Store(false)
	}()

	start := time.Now()
	waitForDashboardDrainWithTiming(context.Background(), func() int {
		if connected.Load() {
			return 1
		}
		return 0
	}, 10*time.Millisecond, time.Second, 5*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 60*time.Millisecond {
		t.Fatalf("returned before client disconnected: %v", elapsed)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("kept waiting long after client disconnected: %v", elapsed)
	}
}

func TestWaitForDashboardDrain_StopsAtMaxGraceIfClientNeverDisconnects(t *testing.T) {
	start := time.Now()
	waitForDashboardDrainWithTiming(context.Background(), func() int { return 1 }, 10*time.Millisecond, 50*time.Millisecond, 5*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 50*time.Millisecond {
		t.Fatalf("returned before maxGrace elapsed: %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("did not stop at maxGrace: %v", elapsed)
	}
}

func TestWaitForDashboardDrain_CtxCancel_ReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	waitForDashboardDrainWithTiming(ctx, func() int { return 1 }, time.Second, time.Minute, 5*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("cancelled ctx did not short-circuit wait: %v", elapsed)
	}
}
