package ring

type Ring[T any] struct {
	buf   []T
	next  int
	count int
}

func New[T any](capacity int) *Ring[T] {
	if capacity < 1 {
		panic("ring: capacity must be at least 1")
	}
	return &Ring[T]{buf: make([]T, capacity)}
}

func (r *Ring[T]) Push(v T) {
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

func (r *Ring[T]) Len() int {
	return r.count
}

func (r *Ring[T]) Cap() int {
	return len(r.buf)
}

func (r *Ring[T]) Snapshot() []T {
	out := make([]T, 0, r.count)
	oldest := (r.next - r.count + len(r.buf)) % len(r.buf)
	for i := range r.count {
		out = append(out, r.buf[(oldest+i)%len(r.buf)])
	}
	return out
}
