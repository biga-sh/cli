package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Good styles text as a positive state.
func Good(s string) string { return green.Render(s) }

// Faint styles text as secondary.
func Faint(s string) string { return gray.Render(s) }

// Table prints rows as aligned columns inside the frame, under an optional
// header. Cells may already be styled.
func (u *UI) Table(header []string, rows [][]string) {
	all := rows
	if header != nil {
		styled := make([]string, len(header))
		for i, h := range header {
			styled[i] = gray.Render(h)
		}
		all = append([][]string{styled}, rows...)
	}

	var widths []int
	for _, row := range all {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}

	lines := make([]string, len(all))
	for r, row := range all {
		var b strings.Builder
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(cell)+2))
			}
		}
		lines[r] = strings.TrimRight(b.String(), " ")
	}
	u.Message(strings.Join(lines, "\n"))
}
