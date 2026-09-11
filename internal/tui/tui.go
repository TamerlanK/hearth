package tui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/TamerlanK/hearth/pkg/client"
	tea "github.com/charmbracelet/bubbletea"
)

func Run(ctx context.Context, c *client.Client, addr, name string, bell bool) error {
	m := newModel(c, addr, name, os.Getenv("NO_COLOR") == "")
	m.bell = bell
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	if _, err := p.Run(); err != nil && !isCancelled(ctx, err) {
		return fmt.Errorf("terminal ui: %w", err)
	}
	return nil
}

func isCancelled(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled)
}
