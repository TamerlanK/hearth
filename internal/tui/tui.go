package tui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/TamerlanK/hearth/pkg/client"
	tea "github.com/charmbracelet/bubbletea"
)

// Options configure the terminal UI.
type Options struct {
	Addr    string
	Name    string
	Bell    bool
	LogFile string
}

// Run draws the chat client until the context is cancelled or the user quits.
func Run(ctx context.Context, c *client.Client, opts Options) error {
	m := newModel(c, opts.Addr, opts.Name, os.Getenv("NO_COLOR") == "")
	m.bell = opts.Bell
	if opts.LogFile != "" {
		f, err := os.OpenFile(opts.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open --log-file: %w", err)
		}
		defer func() {
			if cerr := f.Close(); cerr != nil {
				fmt.Fprintln(os.Stderr, "hearth: close log file:", cerr)
			}
		}()
		m.journal = f
	}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	if _, err := p.Run(); err != nil && !isCancelled(ctx, err) {
		return fmt.Errorf("terminal ui: %w", err)
	}
	return nil
}

func isCancelled(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled)
}
