package main

import (
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// The desktop's own faces: its sans face for the interface and its
// monospaced face for the terminal. On Linux, fontconfig resolves these to
// the families the user configured for Latin and Han text; on macOS and
// Windows they are the system faces.
const (
	uiFont       = "sans-serif"
	terminalFont = "monospace"
)

// theme returns the app's look for the desktop's light or dark appearance.
func (a *app) theme(base *ui.Theme) *ui.Theme {
	t := *base
	t.Font = uiFont
	t.Spacing = 4
	t.Radius = 8
	t.ScrollbarWidth = 7

	if t.Dark {
		t.Background = ui.Hex("#0b0c0e")
		t.Surface = ui.Hex("#121316")
		t.SurfaceHover = ui.Hex("#1a1c20")
		t.SurfacePressed = ui.Hex("#21242a")
		t.Border = ui.Hex("#23252a")
		t.Text = ui.Hex("#e9eaec")
		t.TextMuted = ui.Hex("#8d9198")
		t.Accent = ui.Hex("#5fe3bc")
		t.AccentHover = ui.Hex("#7beccd")
		t.AccentPressed = ui.Hex("#4bd1a9")
		t.AccentText = ui.Hex("#062019")
		t.Danger = ui.Hex("#ff7b86")
		t.Warning = ui.Hex("#e6c07b")
		t.Success = ui.Hex("#5fe3bc")
		t.Selection = ui.Hex("#1c3a32")
		t.Focus = ui.Hex("#5fe3bc")
		t.Inverse = ui.Hex("#f1f2f3")
		t.InverseText = ui.Hex("#141518")
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.18)
		return &t
	}

	t.Background = ui.Hex("#fdfdfc")
	t.Surface = ui.Hex("#f4f4f1")
	t.SurfaceHover = ui.Hex("#ebebe7")
	t.SurfacePressed = ui.Hex("#e1e1dc")
	t.Border = ui.Hex("#e6e6e0")
	t.Text = ui.Hex("#17181a")
	t.TextMuted = ui.Hex("#777b81")
	t.Accent = ui.Hex("#0e8a6b")
	t.AccentHover = ui.Hex("#0b7a5f")
	t.AccentPressed = ui.Hex("#096a53")
	t.AccentText = ui.Hex("#ffffff")
	t.Danger = ui.Hex("#c0392b")
	t.Warning = ui.Hex("#9a6b00")
	t.Success = ui.Hex("#0e8a6b")
	t.Selection = ui.Hex("#d8f2e9")
	t.Focus = ui.Hex("#0e8a6b")
	t.Inverse = ui.Hex("#17181a")
	t.InverseText = ui.Hex("#fdfdfc")
	t.Scrollbar = ui.RGBA(0, 0, 0, 0.18)
	return &t
}

// terminalTheme is always dark: a terminal reads as a terminal, even in a
// light window.
func terminalTheme() *terminal.Theme {
	return &terminal.Theme{
		Foreground:    ui.Hex("#d6d9df"),
		Background:    ui.Hex("#0d0f12"),
		Cursor:        ui.Hex("#5fe3bc"),
		CursorText:    ui.Hex("#0d0f12"),
		Selection:     ui.Hex("#2b4038"),
		SelectionText: ui.Hex("#eef7f3"),
		Palette: [16]ui.Color{
			ui.Hex("#1b1e24"),
			ui.Hex("#e06c75"),
			ui.Hex("#88c999"),
			ui.Hex("#e6c07b"),
			ui.Hex("#82aaff"),
			ui.Hex("#c792ea"),
			ui.Hex("#7dcfff"),
			ui.Hex("#c0c6d0"),
			ui.Hex("#565d68"),
			ui.Hex("#ff7b86"),
			ui.Hex("#a9e8b8"),
			ui.Hex("#f5d38a"),
			ui.Hex("#9bb8ff"),
			ui.Hex("#d9a8ff"),
			ui.Hex("#a4e5ff"),
			ui.Hex("#eef1f5"),
		},
	}
}
