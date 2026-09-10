package ring

import "testing"

func BenchmarkRing(b *testing.B) {
	b.Run("push", func(b *testing.B) {
		r := New[[2]uint64](50)
		b.ReportAllocs()
		for i := range b.N {
			r.Push([2]uint64{uint64(i), uint64(i)})
		}
	})
	b.Run("snapshot", func(b *testing.B) {
		r := New[[2]uint64](50)
		for i := range 100 {
			r.Push([2]uint64{uint64(i), uint64(i)})
		}
		b.ReportAllocs()
		for b.Loop() {
			if got := r.Snapshot(); len(got) != 50 {
				b.Fatalf("len = %d", len(got))
			}
		}
	})
}
