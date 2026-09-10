package protocol

import "time"

const (
	Version      = "hearth/1"
	Hello        = "HELLO hearth/1 json"
	MaxLineBytes = 4096
)

type Kind string

const (
	Msg     Kind = "msg"
	PrivMsg Kind = "privmsg"
	System  Kind = "system"
	Join    Kind = "join"
	Leave   Kind = "leave"
	Nick    Kind = "nick"
	Who     Kind = "who"
	Rooms   Kind = "rooms"
	History Kind = "history"
	Error   Kind = "error"
	Pong    Kind = "pong"
)

type Event struct {
	Kind  Kind      `json:"kind"`
	Room  string    `json:"room,omitempty"`
	From  string    `json:"from,omitempty"`
	To    string    `json:"to,omitempty"`
	Text  string    `json:"text,omitempty"`
	Names []string  `json:"names,omitempty"`
	Time  time.Time `json:"time"`
	Seq   uint64    `json:"seq,omitempty"`
}

type Command struct {
	Name string
	Args []string
	Text string
}

var kinds = map[Kind]struct{}{
	Msg: {}, PrivMsg: {}, System: {}, Join: {}, Leave: {}, Nick: {},
	Who: {}, Rooms: {}, History: {}, Error: {}, Pong: {},
}

func KnownKind(k Kind) bool {
	_, ok := kinds[k]
	return ok
}
