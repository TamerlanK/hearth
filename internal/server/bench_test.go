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

func BenchmarkConnectStorm(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("clients=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				b.StopTimer()
				h := newHub(Config{HistorySize: defaultHistorySize, DefaultRoom: defaultRoom})
				clients := make([]*client, n)
				for i := range clients {
					clients[i] = newClient(nil, Config{})
				}
				b.StartTimer()
				for i, c := range clients {
					if err := h.add(c, fmt.Sprintf("c%d", i)); err != nil {
						b.Fatalf("add c%d: %v", i, err)
					}
				}
			}
		})
	}
}

func BenchmarkPrivateMessage(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("online=%d", n), func(b *testing.B) {
			h := newHub(Config{HistorySize: defaultHistorySize, DefaultRoom: defaultRoom})
			clients := make([]*client, n)
			for i := range clients {
				clients[i] = newClient(nil, Config{})
				if err := h.add(clients[i], fmt.Sprintf("c%d", i)); err != nil {
					b.Fatalf("add c%d: %v", i, err)
				}
			}
			drainOutboxes(clients)
			from, to := clients[0], clients[n-1]
			cmd := protocol.Command{Name: "msg", Args: []string{to.name}, Text: "psst"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				h.private(from, cmd)
				if i%sendBuffer == sendBuffer-1 {
					b.StopTimer()
					drainOutboxes([]*client{to})
					b.StartTimer()
				}
			}
		})
	}
}
