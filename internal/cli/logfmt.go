package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

const (
	logClock  = "15:04:05"
	msgColumn = 26
)

type prettyHandler struct {
	w      io.Writer
	mu     *sync.Mutex
	level  slog.Leveler
	color  bool
	prefix string
	attrs  []byte
}

func newPrettyHandler(w io.Writer, level slog.Leveler, color bool) *prettyHandler {
	return &prettyHandler{w: w, mu: &sync.Mutex{}, level: level, color: color}
}

func (h *prettyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = bytes.Clone(h.attrs)
	for _, a := range attrs {
		next.attrs = next.appendAttr(next.attrs, h.prefix, a)
	}
	return &next
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.prefix = h.prefix + name + "."
	return &next
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	var b []byte
	if !r.Time.IsZero() {
		b = h.paint(b, dimCode, r.Time.Format(logClock))
		b = append(b, ' ')
	}
	b = h.paint(b, levelCode(r.Level), levelName(r.Level))
	b = append(b, ' ')
	b = append(b, r.Message...)
	tail := bytes.Clone(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		tail = h.appendAttr(tail, h.prefix, a)
		return true
	})
	if n := msgColumn - len(r.Message); n > 0 && len(tail) > 0 {
		b = append(b, strings.Repeat(" ", n)...)
	}
	b = append(b, tail...)
	b = append(b, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(b)
	return err
}

func (h *prettyHandler) appendAttr(b []byte, prefix string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) || a.Key == "event" {
		return b
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			b = h.appendAttr(b, prefix, ga)
		}
		return b
	}
	b = append(b, ' ')
	b = h.paint(b, dimCode, prefix+a.Key+"=")
	return append(b, quoteIfNeeded(a.Value.String())...)
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		if unicode.IsSpace(r) || r == '"' || r == '=' || !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

const (
	dimCode   = "2"
	blueCode  = "34"
	greenCode = "32"
	warnCode  = "33"
	redCode   = "31"
)

func (h *prettyHandler) paint(b []byte, code, s string) []byte {
	if !h.color {
		return append(b, s...)
	}
	b = append(b, "\x1b["...)
	b = append(b, code...)
	b = append(b, 'm')
	b = append(b, s...)
	return append(b, "\x1b[0m"...)
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO "
	case l < slog.LevelError:
		return "WARN "
	default:
		return "ERROR"
	}
}

func levelCode(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return dimCode
	case l < slog.LevelWarn:
		return greenCode
	case l < slog.LevelError:
		return warnCode
	default:
		return redCode
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func wantColor(w io.Writer) bool {
	return os.Getenv("NO_COLOR") == "" && isTerminal(w)
}
