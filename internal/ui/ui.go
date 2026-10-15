// Package ui renders the CLI's terminal output: a framed log of steps in the
// style of @clack/prompts, spinners, and the interactive command picker.
package ui

import (
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"
)

var (
	gray    = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	blue    = lipgloss.NewStyle().Foreground(lipgloss.Blue)
	green   = lipgloss.NewStyle().Foreground(lipgloss.Green)
	yellow  = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	red     = lipgloss.NewStyle().Foreground(lipgloss.Red)
	magenta = lipgloss.NewStyle().Foreground(lipgloss.Magenta)
	cyan    = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	bold    = lipgloss.NewStyle().Bold(true)
)

type symbols struct {
	bar, barStart, barEnd              string
	info, success, warn, failure, done string
	frames                             []string
}

var (
	unicodeSymbols = symbols{
		bar: "│", barStart: "┌", barEnd: "└",
		info: "●", success: "◆", warn: "▲", failure: "■", done: "◇",
		frames: []string{"◒", "◐", "◓", "◑"},
	}
	asciiSymbols = symbols{
		bar: "|", barStart: "T", barEnd: "—",
		info: "•", success: "*", warn: "!", failure: "x", done: "o",
		frames: []string{"•", "o", "O", "0"},
	}
)

// UI writes framed output to a terminal or any other writer. It is safe for
// concurrent use, so several broadcasts can log through one UI.
type UI struct {
	mu   sync.Mutex
	out  io.Writer
	tty  bool
	sym  symbols
	spin *Spinner // the spinner currently drawn on the last line, if any
}

// New returns a UI writing to f. Colors follow the terminal's capabilities
// and the NO_COLOR / CLICOLOR conventions; animation is used only on a TTY.
func New(f *os.File) *UI {
	lipgloss.EnableLegacyWindowsANSI(f)
	sym := unicodeSymbols
	if !unicodeSupported() {
		sym = asciiSymbols
	}
	return &UI{
		out: colorprofile.NewWriter(f, os.Environ()),
		tty: term.IsTerminal(int(f.Fd())),
		sym: sym,
	}
}

// NewPlain returns a UI without color or animation, for tests and pipes.
func NewPlain(w io.Writer) *UI {
	return &UI{out: &colorprofile.Writer{Forward: w, Profile: colorprofile.NoTTY}, sym: unicodeSymbols}
}

// Interactive reports whether the UI is attached to a terminal.
func (u *UI) Interactive() bool { return u.tty }

// Intro opens the frame.
func (u *UI) Intro(title string) {
	u.print(gray.Render(u.sym.barStart) + "  " + bold.Render(title) + "\n")
}

// Outro closes the frame.
func (u *UI) Outro(msg string) {
	u.print(gray.Render(u.sym.bar) + "\n" + gray.Render(u.sym.barEnd) + "  " + msg + "\n\n")
}

// Cancel closes the frame after the user backed out.
func (u *UI) Cancel(msg string) {
	u.print(gray.Render(u.sym.barEnd) + "  " + red.Render(msg) + "\n\n")
}

// Info logs a neutral step.
func (u *UI) Info(msg string) { u.log(blue.Render(u.sym.info), msg) }

// Success logs a completed step.
func (u *UI) Success(msg string) { u.log(green.Render(u.sym.success), msg) }

// Warn logs a problem that did not stop the command.
func (u *UI) Warn(msg string) { u.log(yellow.Render(u.sym.warn), msg) }

// Error logs a failure.
func (u *UI) Error(msg string) { u.log(red.Render(u.sym.failure), msg) }

// Message logs plain text inside the frame.
func (u *UI) Message(msg string) { u.log(gray.Render(u.sym.bar), msg) }

// Prefixed returns a logger that tags every line, e.g. "[api] Synced …".
// An empty prefix logs lines unchanged.
func (u *UI) Prefixed(prefix string) *Prefixed {
	if prefix != "" {
		prefix = "[" + prefix + "] "
	}
	return &Prefixed{ui: u, prefix: prefix}
}

// log writes a symbol-led entry.
func (u *UI) log(symbol, msg string) { u.print(entry(u.sym, symbol, msg) + "\n") }

// entry formats a symbol-led entry, preceded by a spacer line, keeping
// continuation lines inside the frame. It has no trailing newline.
func entry(sym symbols, symbol, msg string) string {
	var b strings.Builder
	bar := gray.Render(sym.bar)
	b.WriteString(bar)
	for i, line := range strings.Split(msg, "\n") {
		if i == 0 {
			b.WriteString("\n" + symbol + "  " + line)
		} else {
			b.WriteString("\n" + bar + "  " + line)
		}
	}
	return b.String()
}

// print writes s, first clearing an active spinner's line; the spinner
// redraws itself below on its next frame.
func (u *UI) print(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.spin != nil {
		io.WriteString(u.out, "\r\x1b[K")
	}
	io.WriteString(u.out, s)
}

// Prefixed is a logger that tags every line with a project name.
type Prefixed struct {
	ui     *UI
	prefix string
}

func (p *Prefixed) Info(msg string)    { p.ui.Info(p.prefix + msg) }
func (p *Prefixed) Success(msg string) { p.ui.Success(p.prefix + msg) }
func (p *Prefixed) Warn(msg string)    { p.ui.Warn(p.prefix + msg) }
func (p *Prefixed) Error(msg string)   { p.ui.Error(p.prefix + msg) }
func (p *Prefixed) Message(msg string) { p.ui.Message(p.prefix + msg) }

// unicodeSupported mirrors the is-unicode-supported heuristic: everything but
// the legacy Windows console renders box-drawing characters.
func unicodeSupported() bool {
	if runtime.GOOS != "windows" {
		return os.Getenv("TERM") != "linux"
	}
	return os.Getenv("WT_SESSION") != "" ||
		os.Getenv("TERMINUS_SUBLIME") != "" ||
		os.Getenv("ConEmuTask") == "{cmd::Cmder}" ||
		os.Getenv("TERM_PROGRAM") == "vscode" ||
		os.Getenv("TERM") == "xterm-256color" ||
		os.Getenv("TERM") == "alacritty" ||
		os.Getenv("TERMINAL_EMULATOR") == "JetBrains-JediTerm"
}
