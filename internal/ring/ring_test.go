package ring

import (
	"slices"
	"testing"
)

func TestPushAndSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		push     []int
		want     []int
	}{
		{"empty", 3, nil, []int{}},
		{"partial", 3, []int{1, 2}, []int{1, 2}},
		{"exactly full", 3, []int{1, 2, 3}, []int{1, 2, 3}},
		{"one overwrite", 3, []int{1, 2, 3, 4}, []int{2, 3, 4}},
		{"many overwrites", 3, []int{1, 2, 3, 4, 5, 6, 7}, []int{5, 6, 7}},
		{"capacity one", 1, []int{1, 2, 3}, []int{3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New[int](tt.capacity)
			for _, v := range tt.push {
				r.Push(v)
			}
			if got := r.Snapshot(); !slices.Equal(got, tt.want) {
				t.Errorf("Snapshot() = %v, want %v", got, tt.want)
			}
			if got := r.Len(); got != len(tt.want) {
				t.Errorf("Len() = %d, want %d", got, len(tt.want))
			}
			if got := r.Cap(); got != tt.capacity {
				t.Errorf("Cap() = %d, want %d", got, tt.capacity)
			}
		})
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	r := New[int](2)
	r.Push(1)
	r.Push(2)
	snap := r.Snapshot()
	snap[0] = 99
	if got := r.Snapshot(); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("mutating a snapshot changed the ring: %v", got)
	}
	r.Push(3)
	if !slices.Equal(snap, []int{99, 2}) {
		t.Errorf("pushing changed an earlier snapshot: %v", snap)
	}
}

func TestNewRejectsEmptyCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New[int](%d) did not panic", capacity)
				}
			}()
			New[int](capacity)
		}()
	}
}
