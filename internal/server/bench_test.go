package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/TamerlanK/hearth/pkg/protocol"
)

func BenchmarkFanout(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("clients=%d", n), func(b *testing.B) {
			h := newHub(Config{HistorySize: defaultHistorySize, DefaultRoom: defaultRoom})
			r := h.rooms[defaultRoom]
			clients := make([]*client, n)
			for i := range clients {
				c := newClient(nil, Config{})
				c.name = fmt.Sprintf("c%d", i)
				c.room = r
				r.members[c] = struct{}{}
				clients[i] = c
			}
			e := protocol.Event{Kind: protocol.Msg, Room: defaultRoom, From: "c0", Text: "hello everyone", Time: time.Now()}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				h.broadcast(r, e)
				if i%sendBuffer == sendBuffer-1 {
					b.StopTimer()
					drainOutboxes(clients)
					b.StartTimer()
				}
			}
			b.StopTimer()
			drainOutboxes(clients)
			for _, c := range clients {
				if c.droppedCount() != 0 {
					b.Fatalf("%s dropped %d events, benchmark did not drain fast enough", c.name, c.droppedCount())
				}
			}
		})
	}
}

func drainOutboxes(clients []*client) {
	for _, c := range clients {
		for len(c.send) > 0 {
			<-c.send
		}
	}
}
