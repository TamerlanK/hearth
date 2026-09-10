// Package tui renders the hearth chat client with bubbletea, following the Elm
// architecture: Model holds every piece of state, Update is the only place that
// mutates it, and View is a pure function from Model to a string.
//
// # Model, Update, View
//
// [Model] owns the per-room transcripts, the room list, the user list, the
// connection state and the two bubbles widgets (a viewport for the transcript
// and a textinput for the prompt). Update receives one tea.Msg at a time and
// returns the next Model plus a tea.Cmd; it never blocks and never touches the
// network directly. Work that can block — writing a command to the server,
// closing the client — is deferred to a tea.Cmd, which bubbletea runs on its
// own goroutine and whose result comes back as another message. View reads the
// model and returns the frame; layout.go holds the sizing, wrapping and badge
// arithmetic as pure functions so the geometry can be tested without a
// terminal, and styles.go holds every lipgloss style in one place.
//
// # Bridging client events
//
// [github.com/TamerlanK/hearth/pkg/client.Client] delivers server events on a
// channel, which no goroutine may drain into the model directly. Instead the
// await method returns a tea.Cmd that blocks on a single receive and turns the
// event into an eventMsg (or streamClosedMsg when the channel closes). Update
// handles that message and re-issues await as part of its returned command, so
// exactly one receive is ever in flight and every event reaches the model
// through the same serialised path as a keystroke.
//
// Connection state has no channel of its own, so poll returns a tea.Cmd that
// ticks once a second, reads Client.State and Client.Stats and returns a
// linkMsg. That drives the status bar and the "reconnecting… (attempt N)"
// banner, and it lets Update reject a send with an inline error instead of
// letting the write hang while the client is retrying its dial.
package tui
