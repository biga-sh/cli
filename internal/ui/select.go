package ui

import (
	"context"
	"errors"

	"charm.land/huh/v2"
)

// ErrAborted is returned when the user backs out of a prompt.
var ErrAborted = errors.New("aborted")

// Choice is one option of a [UI.Select] prompt.
type Choice struct {
	Value string
	Label string
	Hint  string
}

// Select asks the user to pick one of choices and returns its value.
func (u *UI) Select(ctx context.Context, title string, choices []Choice) (string, error) {
	options := make([]huh.Option[string], len(choices))
	for i, c := range choices {
		label := c.Label
		if c.Hint != "" {
			label += "  " + gray.Render(c.Hint)
		}
		options[i] = huh.NewOption(label, c.Value)
	}

	var value string
	field := huh.NewSelect[string]().Title(title).Options(options...).Value(&value)
	err := huh.NewForm(huh.NewGroup(field)).WithShowHelp(false).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) {
		return "", ErrAborted
	}
	return value, err
}
