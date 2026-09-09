package ratelimit

import (
	"testing"
	"time"
)

type clock struct {
	t time.Time
}

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestBucket(rate float64, burst int) (*Bucket, *clock) {
	c := &clock{t: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	return New(rate, burst, c.now), c
}

func TestBurstThenEmpty(t *testing.T) {
	b, _ := newTestBucket(5, 10)
	for i := range 10 {
		if !b.Allow() {
			t.Fatalf("call %d denied inside the burst of 10", i)
		}
	}
	if b.Allow() {
		t.Error("call 11 allowed with an empty bucket and no time passed")
	}
}

func TestRefill(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"nothing", 0, 0},
		{"half a token", 100 * time.Millisecond, 0},
		{"one token", 200 * time.Millisecond, 1},
		{"two tokens", 400 * time.Millisecond, 2},
		{"capped at burst", time.Hour, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, c := newTestBucket(5, 10)
			for b.Allow() {
			}
			c.add(tt.elapsed)
			got := 0
			for b.Allow() {
				got++
			}
			if got != tt.want {
				t.Errorf("after %s the bucket allowed %d, want %d", tt.elapsed, got, tt.want)
			}
		})
	}
}

func TestSteadyState(t *testing.T) {
	b, c := newTestBucket(5, 10)
	for b.Allow() {
	}
	allowed := 0
	for range 50 {
		c.add(100 * time.Millisecond)
		if b.Allow() {
			allowed++
		}
	}
	if want := 25; allowed != want {
		t.Errorf("over 5s at 10 calls/s a 5/s bucket allowed %d, want %d", allowed, want)
	}
}

func TestZeroRateNeverRefills(t *testing.T) {
	b, c := newTestBucket(0, 1)
	if !b.Allow() {
		t.Fatal("the single burst token was denied")
	}
	c.add(time.Hour)
	if b.Allow() {
		t.Error("a zero-rate bucket refilled")
	}
}

func TestNilClockUsesWallTime(t *testing.T) {
	b := New(5, 1, nil)
	if !b.Allow() {
		t.Error("first call denied")
	}
	if b.Allow() {
		t.Error("second call allowed with burst 1")
	}
}
