package tui

import (
	"hash/fnv"

	"github.com/charmbracelet/lipgloss"
)

type styles struct {
	sidebarTitle lipgloss.Style
	room         lipgloss.Style
	roomCurrent  lipgloss.Style
	badge        lipgloss.Style
	self         lipgloss.Style
	user         lipgloss.Style
	clock        lipgloss.Style
	text         lipgloss.Style
	own          lipgloss.Style
	system       lipgloss.Style
	private      lipgloss.Style
	failure      lipgloss.Style
	divider      lipgloss.Style
	pill         lipgloss.Style
	status       lipgloss.Style
	statusAlert  lipgloss.Style
	prompt       lipgloss.Style
	overlay      lipgloss.Style
	overlayKey   lipgloss.Style
	focused      lipgloss.Style
	palette      []lipgloss.AdaptiveColor
}

var (
	dim    = lipgloss.AdaptiveColor{Light: "245", Dark: "241"}
	muted  = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	accent = lipgloss.AdaptiveColor{Light: "27", Dark: "39"}
	warn   = lipgloss.AdaptiveColor{Light: "166", Dark: "214"}
	danger = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	ok     = lipgloss.AdaptiveColor{Light: "28", Dark: "78"}
	barFg  = lipgloss.AdaptiveColor{Light: "255", Dark: "236"}
	barBg  = lipgloss.AdaptiveColor{Light: "24", Dark: "111"}
	names  = []lipgloss.AdaptiveColor{
		{Light: "125", Dark: "212"},
		{Light: "22", Dark: "84"},
		{Light: "94", Dark: "215"},
		{Light: "26", Dark: "117"},
		{Light: "90", Dark: "141"},
		{Light: "30", Dark: "80"},
		{Light: "130", Dark: "180"},
		{Light: "53", Dark: "177"},
	}
)

func newStyles(color bool) styles {
	fg := func(c lipgloss.AdaptiveColor) lipgloss.Style {
		if !color {
			return lipgloss.NewStyle()
		}
		return lipgloss.NewStyle().Foreground(c)
	}
	bar := lipgloss.NewStyle()
	if color {
		bar = bar.Foreground(barFg).Background(barBg)
	} else {
		bar = bar.Reverse(true)
	}
	s := styles{
		sidebarTitle: fg(muted).Bold(true),
		room:         fg(muted),
		roomCurrent:  fg(accent).Bold(true),
		badge:        fg(warn).Bold(true),
		self:         fg(ok).Bold(true),
		user:         fg(muted),
		clock:        fg(dim),
		text:         lipgloss.NewStyle(),
		own:          fg(ok).Bold(true),
		system:       fg(dim).Italic(true),
		private:      fg(warn),
		failure:      fg(danger).Bold(true),
		divider:      fg(dim),
		pill:         fg(warn).Bold(true),
		status:       bar,
		statusAlert:  bar.Bold(true),
		prompt:       fg(accent),
		overlay:      fg(muted),
		overlayKey:   fg(accent).Bold(true),
		focused:      fg(accent),
		palette:      names,
	}
	if !color {
		s.palette = nil
	}
	return s
}

func (s *styles) name(who string) lipgloss.Style {
	i := paletteIndex(who, len(s.palette))
	if i < 0 {
		return lipgloss.NewStyle().Bold(true)
	}
	return lipgloss.NewStyle().Foreground(s.palette[i]).Bold(true)
}

func paletteIndex(who string, size int) int {
	if size <= 0 {
		return -1
	}
	h := fnv.New32a()
	if _, err := h.Write([]byte(who)); err != nil {
		return -1
	}
	return int(h.Sum32() % uint32(size))
}
