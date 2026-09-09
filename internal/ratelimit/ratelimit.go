package ratelimit

import "time"

type Bucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func New(rate float64, burst int, now func() time.Time) *Bucket {
	if now == nil {
		now = time.Now
	}
	if rate < 0 {
		rate = 0
	}
	if burst < 0 {
		burst = 0
	}
	return &Bucket{
		rate:   rate,
		burst:  float64(burst),
		tokens: float64(burst),
		last:   now(),
		now:    now,
	}
}

func (b *Bucket) Allow() bool {
	t := b.now()
	if elapsed := t.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+elapsed*b.rate)
		b.last = t
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (b *Bucket) Tokens() float64 {
	return b.tokens
}
