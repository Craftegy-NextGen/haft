//go:build windows

// Interactive terminal dashboard for Windows using tcell.
package ui

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

// ViewRenderer returns formatted text for a named view at the given terminal width.
type ViewRenderer func(viewIndex int, width int) string

var ViewNames = [5]string{"Overview", "Decisions", "Problems", "Coverage", "Evidence"}

// RunInteractive launches the full-screen interactive dashboard using tcell.
// Blocks until the user presses q or Esc.
// renderView(0..4) returns the content for that view.
// refresh() reloads data (called on 'r' and auto-timer).
func RunInteractive(renderView ViewRenderer, refresh func() error) error {
	// Create screen
	s, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("create screen: %w", err)
	}
	defer s.Fini()

	// Set up alternate screen buffer
	if err := s.Init(); err != nil {
		return fmt.Errorf("init screen: %w", err)
	}

	// Clear and show cursor
	s.Clear()
	s.ShowCursor(0, 0)

	// Handle signals for Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, os.Kill)
	defer signal.Stop(sigCh)

	current := 0

	render := func() {
		w, h := s.Size()
		content := renderView(current, w)

		// Clear screen
		s.Clear()

		// Draw tab bar
		for i, name := range ViewNames {
			col := 1 + i*18
			if i == current {
				s.SetContent(col, 0, '[', nil, tcell.StyleDefault.Foreground(tcell.ColorTeal).Bold(true))
				s.SetContent(col+1, 0, rune('1'+i), nil, tcell.StyleDefault.Foreground(tcell.ColorTeal).Bold(true))
				s.SetContent(col+2, 0, ' ', nil, tcell.StyleDefault.Foreground(tcell.ColorTeal).Bold(true))
				for _, ch := range name {
					s.SetContent(col+3, 0, ch, nil, tcell.StyleDefault.Foreground(tcell.ColorTeal).Bold(true))
					col++
				}
				s.SetContent(col+3, 0, ']', nil, tcell.StyleDefault.Foreground(tcell.ColorTeal).Bold(true))
			} else {
				s.SetContent(col, 0, ' ', nil, tcell.StyleDefault.Foreground(tcell.ColorDarkGray))
				s.SetContent(col+1, 0, rune('1'+i), nil, tcell.StyleDefault.Foreground(tcell.ColorDarkGray))
				s.SetContent(col+2, 0, ' ', nil, tcell.StyleDefault.Foreground(tcell.ColorDarkGray))
				for _, ch := range name {
					s.SetContent(col+3, 0, ch, nil, tcell.StyleDefault.Foreground(tcell.ColorDarkGray))
					col++
				}
				s.SetContent(col+3, 0, ' ', nil, tcell.StyleDefault.Foreground(tcell.ColorDarkGray))
			}
		}

		// Draw separator line
		for x := 0; x < w; x++ {
			s.SetContent(x, 1, '─', nil, tcell.StyleDefault.Foreground(tcell.ColorDarkGray))
		}

		// Draw content
		lines := strings.Split(content, "\n")
		maxLines := h - 3 // Reserve footer row
		if maxLines > 0 {
			if len(lines) > maxLines {
				lines = lines[:maxLines]
			}
			for y, line := range lines {
				for x, ch := range line {
					if x < w {
						s.SetContent(x, y+2, ch, nil, tcell.StyleDefault)
					}
				}
			}
		}

		// Draw footer
		footer := " 1-5 switch | <- -> navigate | r refresh | q/Esc close "
		for x, ch := range footer {
			if x < w {
				s.SetContent(x, h-1, ch, nil, tcell.StyleDefault.Foreground(tcell.ColorDarkGray))
			}
		}

		s.Show()
	}

	// Initial render
	render()

	// Auto-refresh every 5 seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Main event loop using polling
	for {
		// Check for events with timeout
		ev := s.PollEvent()
		if ev != nil {
			switch ev := ev.(type) {
			case *tcell.EventKey:
				switch ev.Key() {
				case tcell.KeyEsc, tcell.KeyCtrlC:
					return nil
				case tcell.KeyLeft:
					current = (current + 4) % 5
					render()
				case tcell.KeyRight:
					current = (current + 1) % 5
					render()
				}
				switch ev.Rune() {
				case 'q', 'Q':
					return nil
				case '1':
					current = 0
					render()
				case '2':
					current = 1
					render()
				case '3':
					current = 2
					render()
				case '4':
					current = 3
					render()
				case '5':
					current = 4
					render()
				case 'r', 'R':
					_ = refresh()
					render()
				}
			case *tcell.EventResize:
				render()
			}
		}

		// Check ticker
		select {
		case <-ticker.C:
			_ = refresh()
			render()
		default:
			// No ticker event, continue
		}

		// Check signals
		select {
		case <-sigCh:
			return nil
		default:
			// No signal
		}
	}
}

// RunDashboard is a stub on Windows - UI handles the dashboard
func RunDashboard() error {
	return nil
}