// Package protocol defines the hearth/1 wire format: newline-delimited
// commands from client to server and events from server to client. The
// default [TextCodec] is readable from telnet; a client that sends [Hello] as
// its first line switches the connection to [JSONCodec]. The full
// specification is docs/PROTOCOL.md in the repository.
package protocol

import (
	"strings"
	"time"
)

// Version names the protocol, Hello is the exact first line that negotiates
// the JSON encoding, and MaxLineBytes is the longest line either side accepts
// before answering with [ErrLineTooLong]. A server that requires a token
// expects [HelloWith] instead of Hello.
const (
	Version      = "hearth/1"
	Hello        = "HELLO hearth/1 json"
	MaxLineBytes = 4096
	tokenField   = " token="
)

// HelloWith returns the negotiation line carrying a token. With an empty
// token it returns [Hello].
func HelloWith(token string) string {
	if token == "" {
		return Hello
	}
	return Hello + tokenField + token
}

// ParseHello reports whether line is a negotiation line, and returns the
// token it carries, which is empty when it carries none.
func ParseHello(line string) (token string, ok bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == Hello {
		return "", true
	}
	return strings.CutPrefix(line, Hello+tokenField)
}

// Kind says what an [Event] describes.
type Kind string

// The event kinds a server sends. Msg and PrivMsg carry chat text and History
// is a Msg replayed from a room's buffer; Who and Rooms answer in Names; Error
// is the server refusing the client's previous command.
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
	Away    Kind = "away"
)

// Event is one line from the server. Which fields are set depends on Kind:
// Room, From and Text for Msg and History; From, To and Text for PrivMsg;
// From and To for Nick; Names for Who and Rooms; Text for System and Error.
// Seq numbers the events on one connection from 1 and Time is the server's
// clock.
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

// Command is one decoded line from the client. Args holds the leading words
// a command takes, such as the room for join or the recipient for msg, and
// Text the free text after them.
type Command struct {
	Name string
	Args []string
	Text string
}

var kinds = map[Kind]struct{}{
	Msg: {}, PrivMsg: {}, System: {}, Join: {}, Leave: {}, Nick: {},
	Who: {}, Rooms: {}, History: {}, Error: {}, Pong: {}, Away: {},
}

// KnownKind reports whether k is an event kind defined by hearth/1.
func KnownKind(k Kind) bool {
	_, ok := kinds[k]
	return ok
}
