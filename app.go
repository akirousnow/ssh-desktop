package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

const appName = "SSH Desktop"

type app struct {
	win    atomic.Pointer[mygo.Window]
	closed atomic.Bool

	configPath  string
	hosts       []Host
	selectedID  string
	query       string
	selectedRow int

	searchFocus ui.Handle
	hostsList   ui.ListState
	termHandle  ui.Handle

	sessions    map[string]*session
	attempts    map[string]int
	nextAttempt int
	failures    map[string]*connFailure
	secrets     map[string]string
	passphrases map[string]string

	editorOpen bool
	editor     hostDraft

	confirmOpen bool
	confirmID   string

	promptOpen bool
	prompt     *secretPrompt

	toastMsg  string
	loadErr   string
	lastTitle string
}

type session struct {
	host Host
	conn *sshConn
	term *terminal.Terminal
	dial time.Duration
	done bool
}

type secretKind int

const (
	secretPassword secretKind = iota + 1
	secretPassphrase
)

type secretPrompt struct {
	hostID   string
	kind     secretKind
	value    string
	remember bool
	err      string
}

func newApp(path string) *app {
	a := &app{
		configPath:  path,
		sessions:    map[string]*session{},
		attempts:    map[string]int{},
		failures:    map[string]*connFailure{},
		secrets:     map[string]string{},
		passphrases: map[string]string{},
	}
	a.hostsList.Selected = &a.selectedRow
	a.load()
	return a
}

func (a *app) window() *mygo.Window { return a.win.Load() }

// post runs fn on the UI thread when the window still exists.
func (a *app) post(fn func()) {
	w := a.window()
	if w == nil {
		return
	}
	w.Update(func() {
		if !a.closed.Load() {
			fn()
		}
	})
}

func (a *app) refresh() {
	if w := a.window(); w != nil {
		w.Update(func() {})
	}
}

func (a *app) updateTitle() {
	w := a.window()
	if w == nil {
		return
	}
	title := appName
	if h, ok := a.selectedHost(); ok {
		title = h.displayName() + " · " + appName
	}
	if title == a.lastTitle {
		return
	}
	a.lastTitle = title
	w.SetTitle(title)
}

func (a *app) shortcutLabel(letter string) string {
	if runtime.GOOS == "darwin" {
		return "⌘ " + letter
	}
	return "Ctrl " + letter
}

func (a *app) menu() *mygo.Menu {
	newHostAccel := ""
	if runtime.GOOS == "darwin" {
		// On other platforms Ctrl+N belongs to the shell while a terminal
		// has the focus; the view handles it only when the terminal does not.
		newHostAccel = "Cmd+N"
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "New Host…", Accelerator: newHostAccel, Click: func(*mygo.MenuItem, *mygo.Window) {
				a.newHost()
				a.refresh()
			}},
			{Label: "Connect", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.connectSelected()
			}},
			{Label: "Disconnect", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.disconnectSelected()
				a.refresh()
			}},
			mygo.Separator(),
			{Role: mygo.RoleClose},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{Label: "Search Hosts", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.searchFocus.Focus()
				a.refresh()
			}},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}

func (a *app) view(c *ui.Context) {
	c.SetTheme(a.theme(c.Theme()))
	if !a.termHandle.Focused(c) {
		if c.Shortcut(ui.Cmd, ui.KeyK) {
			a.searchFocus.Focus()
		}
		if c.Shortcut(ui.Cmd, ui.KeyN) {
			a.newHost()
		}
	}

	t := c.Theme()
	ui.Row(c).Fill().Children(func() {
		a.sidebar(c)
		ui.Box(c).Width(1).FillHeight().Background(t.Border)
		ui.Column(c).Grow(1).MinWidth(0).FillHeight().Background(t.Background).Children(func() {
			a.content(c)
		})
	})

	a.dialogs(c)
	if a.toastMsg != "" {
		c.Toast(a.toastMsg)
		a.toastMsg = ""
	}
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(272).Shrink(0).FillHeight().Background(t.Surface).Padding(14, 12).Gap(10).Children(func() {
		a.sidebarHeader(c)
		ui.SearchField(c, &a.query).Bind(&a.searchFocus).Label("Search hosts")

		if len(a.hosts) > 0 {
			ui.Row(c).Padding(4, 4, 0, 4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "HOSTS").FontSize(10).FontWeight(650).LetterSpacing(0.9).TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.Textf(c, "%d", len(a.hosts)).FontSize(10.5).TextColor(t.TextMuted)
			})
		}

		a.hostList(c)
		a.sidebarFooter(c)
	})
}

func (a *app) sidebarHeader(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(30).AlignItems(ui.Center).Padding(0, 4).Children(func() {
		ui.RichText(c,
			ui.Span{Text: "ssh"},
			ui.Span{Text: ".", Color: t.Accent},
		).Font(terminalFont).FontSize(17).FontWeight(650).LetterSpacing(-0.2)
	})
}

func (a *app) hostList(c *ui.Context) {
	rows := a.filteredHosts()
	a.hostsList.Key = func(i int) any { return rows[i].ID }
	a.hostsList.Label = func(i int) string { return rows[i].displayName() }

	selected := -1
	for i := range rows {
		if rows[i].ID == a.selectedID {
			selected = i
			break
		}
	}
	a.selectedRow = selected

	var rowAction func()
	list := ui.List(c, &a.hostsList, len(rows), func(i int) {
		a.hostRow(c, rows[i], &rowAction)
	}).Grow(1)
	if len(rows) == 0 && len(a.hosts) > 0 {
		list.Children(func() {
			ui.Text(c, "No hosts match.").FontSize(12).TextColor(c.Theme().TextMuted).Padding(14, 8)
		})
	}

	if a.selectedRow != selected && a.selectedRow >= 0 && a.selectedRow < len(rows) {
		if id := rows[a.selectedRow].ID; id != a.selectedID {
			a.selectHost(id)
		}
	}
	if list.Submitted() {
		if h, ok := a.selectedHost(); ok {
			a.connect(h)
		}
	}
	if rowAction != nil {
		rowAction()
	}
}

func (a *app) hostRow(c *ui.Context, h Host, pending *func()) {
	t := c.Theme()
	selected := h.ID == a.selectedID
	row := ui.Row(c).Height(48).Padding(0, 10).Gap(9).Radius(8).Cursor(ui.CursorPointer).TextColor(t.Text)
	switch {
	case selected:
		row.Background(t.Selection)
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}

	s := a.sessions[h.ID]
	row.Children(func() {
		ui.Box(c).Size(12, 12).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			switch {
			case s != nil && !s.done:
				ui.Box(c).Size(7, 7).Radius(4).Background(t.Success)
			case s != nil:
				ui.Box(c).Size(6, 6).Radius(3).Background(t.TextMuted)
			case a.attempts[h.ID] != 0:
				ui.Spinner(c).Size(11, 11)
			case a.failures[h.ID] != nil:
				ui.Box(c).Size(7, 7).Radius(4).Background(t.Danger)
			default:
				ui.Box(c).Size(5, 5).Radius(3).Background(t.Border)
			}
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, h.displayName()).FontSize(12.5).FontWeight(550).SingleLine()
			ui.Text(c, h.userName()+"@"+h.displayAddr()).FontSize(10.8).TextColor(t.TextMuted).SingleLine()
		})
	})

	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("Edit…").Chosen() {
			id := h.ID
			*pending = func() { a.editHost(id) }
		}
		if m.Item("Duplicate").Chosen() {
			id := h.ID
			*pending = func() { a.duplicateHost(id) }
		}
		m.Separator()
		if m.Item("Delete…").Chosen() {
			id := h.ID
			*pending = func() { a.confirmDelete(id) }
		}
	})
}

func (a *app) sidebarFooter(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Gap(9).Children(func() {
		ui.Box(c).Height(1).FillWidth().Background(t.Border)
		button := ui.Button(c, "").Label("New host").Height(32).Radius(7).Padding(0, 9).
			Justify(ui.Center).Background(ui.Transparent).BorderWidth(0).
			Tooltip("New host · " + a.shortcutLabel("N"))
		if button.Hovered() {
			button.Background(t.SurfaceHover)
		}
		button.Children(func() {
			ui.Text(c, "+").FontSize(12.5).FontWeight(550)
			ui.Text(c, "New host").FontSize(12.5).FontWeight(550)
		})
		if button.Clicked() {
			a.newHost()
		}
	})
}

func (a *app) content(c *ui.Context) {
	h, ok := a.selectedHost()
	if !ok {
		a.welcome(c)
		return
	}
	if a.attempts[h.ID] != 0 {
		a.connectingPane(c, h)
		return
	}
	if s := a.sessions[h.ID]; s != nil {
		a.sessionPane(c, s)
		return
	}
	a.hostPane(c, h)
}

func (a *app) welcome(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Children(func() {
		ui.Column(c).MaxWidth(440).AlignItems(ui.Center).Children(func() {
			mark := ui.Box(c).Size(58, 58).Radius(17).Background(t.Surface).Border(1, t.Border).
				AlignItems(ui.Center).Justify(ui.Center).
				Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.10))
			mark.Children(func() {
				ui.RichText(c,
					ui.Span{Text: ">", Color: t.Accent},
					ui.Span{Text: "_", Color: t.TextMuted},
				).Font(terminalFont).FontSize(19).FontWeight(650)
			})

			ui.Text(c, "A quiet place for your shells.").FontSize(24).FontWeight(600).
				LetterSpacing(-0.4).TextAlign(ui.Center).Margin(22, 0, 0, 0)
			ui.Text(c, "Save the servers you use, then open a terminal in one click. Everything stays on this machine.").
				FontSize(13).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(370).
				LineHeight(1.45).Margin(8, 0, 0, 0)

			ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(24, 0, 0, 0).Children(func() {
				button := ui.PrimaryButton(c, "").Height(36).Padding(0, 16).Radius(9).Label("New host").Justify(ui.Center)
				button.Children(func() {
					ui.Text(c, "+").FontSize(12.5).FontWeight(600)
					ui.Text(c, "New host").FontSize(12.5).FontWeight(600)
				})
				if button.Clicked() {
					a.newHost()
				}
			})

			ui.Textf(c, "%s new host   ·   %s search", a.shortcutLabel("N"), a.shortcutLabel("K")).
				FontSize(10.5).TextColor(t.TextMuted).Margin(20, 0, 0, 0)
		})
	})
}

func (a *app) connectingPane(c *ui.Context, h Host) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Children(func() {
		ui.Column(c).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(26, 26)
			ui.Textf(c, "Connecting to %s", h.displayName()).FontSize(16).FontWeight(600).
				Margin(18, 0, 0, 0).TextAlign(ui.Center)
			ui.Text(c, h.userName()+"@"+h.displayAddr()).Font(terminalFont).FontSize(11.5).
				TextColor(t.TextMuted).Margin(6, 0, 0, 0)
			ui.Button(c, "Cancel").Margin(20, 0, 0, 0).Height(30).OnClick(func() {
				a.cancelConnect(h.ID)
			})
		})
	})
}

func (a *app) hostPane(c *ui.Context, h Host) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(38, 42).Gap(0).Children(func() {
		ui.Row(c).AlignItems(ui.Start).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(7).Children(func() {
				ui.Text(c, h.displayName()).FontSize(25).FontWeight(650).LetterSpacing(-0.4).SingleLine()
				ui.Text(c, h.userName()+"@"+h.displayAddr()).Font(terminalFont).FontSize(11.8).
					TextColor(t.TextMuted).SingleLine()
			})
			a.iconButton(c, iconPencil, "Edit host", func() { a.editHost(h.ID) })
			more := ui.Button(c, "").Label("More").Width(28).Height(28).Padding(0).Radius(6).
				Background(ui.Transparent).BorderWidth(0)
			if more.Hovered() {
				more.Background(t.SurfaceHover)
			}
			more.Children(func() {
				ui.Icon(c, iconDots).FontSize(16).TextColor(t.TextMuted)
			})
			more.Menu(func(m *ui.Menu) {
				if m.Item("Duplicate").Chosen() {
					a.duplicateHost(h.ID)
				}
				if m.Item("Delete…").Chosen() {
					a.confirmDelete(h.ID)
				}
			})
		})

		if f := a.failures[h.ID]; f != nil {
			a.failureBanner(c, h, f)
		}

		if f := a.failures[h.ID]; f == nil || f.trust == nil {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Margin(22, 0, 0, 0).Children(func() {
				button := ui.PrimaryButton(c, "").Height(36).Padding(0, 16).Radius(9).Label("Connect")
				button.Children(func() {
					ui.Text(c, "Connect").FontSize(12.5).FontWeight(600)
					ui.Icon(c, iconArrow).FontSize(14)
				})
				if button.Clicked() {
					a.connect(h)
				}
			})
		}

		a.hostInfo(c, h)
	})
}

func (a *app) failureBanner(c *ui.Context, h Host, f *connFailure) {
	t := c.Theme()
	banner := ui.Column(c).MaxWidth(620).Radius(10).Background(t.Surface).Border(1, t.Border).
		Padding(14, 16).Gap(6).Margin(22, 0, 0, 0)
	banner.Children(func() {
		ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconAlert).FontSize(14).TextColor(t.Danger)
			ui.Text(c, f.title).FontSize(12.5).FontWeight(650)
		})
		if f.trust == nil {
			ui.Box(c).Absolute().Top(6).Right(6).Children(func() {
				a.iconButton(c, iconX, "Dismiss", func() { delete(a.failures, h.ID) })
			})
		}
		ui.Text(c, f.message).FontSize(12).TextColor(t.TextMuted).LineHeight(1.4)
		if f.detail != "" {
			ui.Text(c, f.detail).Font(terminalFont).FontSize(10.8).TextColor(t.TextMuted).
				Selectable().LineHeight(1.45)
		}
		if f.trust != nil {
			ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(func() {
				if ui.PrimaryButton(c, "Trust and connect").Height(30).Padding(0, 12).Clicked() {
					a.trustAndConnect(h)
				}
				if ui.Button(c, "Cancel").Height(30).Clicked() {
					delete(a.failures, h.ID)
				}
			})
		}
	})
}

func (a *app) hostInfo(c *ui.Context, h Host) {
	t := c.Theme()
	card := ui.Column(c).MaxWidth(620).Margin(30, 0, 0, 0).Radius(12).
		Border(1, t.Border).Background(t.Surface).Padding(2, 16)
	card.Children(func() {
		a.infoRow(c, "Address", h.displayAddr(), true)
		ui.Divider(c)
		a.infoRow(c, "User", h.userName(), false)
		ui.Divider(c)
		a.infoRow(c, "Authentication", h.authLabel(), false)
		if h.Auth == authKey && h.KeyPath != "" {
			ui.Divider(c)
			a.infoRow(c, "Private key", shortPath(expandHome(h.KeyPath)), true)
		}
	})
}

func (a *app) infoRow(c *ui.Context, label, value string, mono bool) {
	t := c.Theme()
	ui.Row(c).Height(40).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Width(132).FontSize(11.8).TextColor(t.TextMuted)
		text := ui.Text(c, value).FontSize(12).SingleLine()
		if mono {
			text.Font(terminalFont).FontSize(11.4)
		}
	})
}

func (a *app) sessionPane(c *ui.Context, s *session) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		bar := ui.Row(c).Height(46).Padding(0, 16).Gap(9).AlignItems(ui.Center).
			Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
		bar.Children(func() {
			color := t.Success
			if s.done {
				color = t.TextMuted
			}
			ui.Box(c).Size(7, 7).Radius(4).Background(color)
			ui.Text(c, s.host.displayName()).FontSize(12.5).FontWeight(600)
			ui.Text(c, s.host.userName()+"@"+s.host.displayAddr()).FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c)
			if s.done {
				a.ghostButton(c, "Reconnect", iconRefresh, func() { a.reconnect(s.host) })
				a.iconButton(c, iconX, "Close terminal", func() { a.closeSession(s.host.ID) })
			} else {
				a.ghostButton(c, "Disconnect", nil, func() { a.disconnectHost(s.host.ID) })
			}
		})
		terminal.View(c, s.term).Grow(1).MinHeight(0).Bind(&a.termHandle)
	})
}

func (a *app) ghostButton(c *ui.Context, label string, ic *ui.SVG, onClick func()) {
	t := c.Theme()
	button := ui.Button(c, "").Label(label).Height(28).Padding(0, 9).Radius(6).
		Background(ui.Transparent).TextColor(t.TextMuted).BorderWidth(0)
	hovered := button.Hovered()
	if hovered {
		button.Background(t.SurfaceHover).TextColor(t.Text)
	}
	button.Children(func() {
		if ic != nil {
			ui.Icon(c, ic).FontSize(13)
		}
		ui.Text(c, label).FontSize(11.5).FontWeight(550)
	})
	if button.Clicked() {
		onClick()
	}
}

func (a *app) iconButton(c *ui.Context, ic *ui.SVG, label string, onClick func()) {
	t := c.Theme()
	button := ui.Button(c, "").Label(label).Tooltip(label).Width(28).Height(28).Padding(0).Radius(6).
		Background(ui.Transparent).BorderWidth(0)
	hovered := button.Hovered()
	if hovered {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() {
		color := t.TextMuted
		if hovered {
			color = t.Text
		}
		ui.Icon(c, ic).FontSize(15).TextColor(color)
	})
	if button.Clicked() {
		onClick()
	}
}

func (a *app) dialogs(c *ui.Context) {
	a.editorDialog(c)
	a.secretDialog(c)
	a.deleteDialog(c)
}

func (a *app) editorDialog(c *ui.Context) {
	if !a.editorOpen {
		return
	}
	t := c.Theme()
	d := &a.editor
	_, errs := d.validate()
	addrErr, userErr, portErr, keyErr := "", "", "", ""
	if d.tried {
		addrErr, userErr, portErr, keyErr = errs.address, errs.user, errs.port, errs.keyPath
	}
	title, action := "New host", "Add host"
	if d.existing {
		title, action = "Edit host", "Save"
	}

	ui.Modal(c, &a.editorOpen, func() {
		if c.Shortcut(0, ui.KeyEnter) {
			a.saveEditor()
		}
		ui.Column(c).Width(430).Gap(13).Children(func() {
			ui.Text(c, title).FontSize(18).FontWeight(650).LetterSpacing(-0.2)

			ui.Field(c, "Name", func() {
				ui.TextInput(c, &d.name).Placeholder("Home server").Label("Name").AutoFocus().OnSubmit(a.saveEditor)
			}).Description("Shown in the sidebar. Defaults to user@host.")

			ui.Field(c, "Host", func() {
				ui.TextInput(c, &d.address).Placeholder("example.com, 10.0.0.2 or user@host:22").Label("Host").OnSubmit(a.saveEditor)
			}).Error(addrErr)

			ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
				ui.Field(c, "User", func() {
					ui.TextInput(c, &d.user).Label("User").OnSubmit(a.saveEditor)
				}).Grow(1).Error(userErr)
				ui.Field(c, "Port", func() {
					ui.TextInput(c, &d.port).Label("Port").OnSubmit(a.saveEditor)
				}).Width(88).Error(portErr)
			})

			ui.Field(c, "Authentication", func() {
				ui.Select(c, &d.auth, []string{"Auto", "SSH agent", "Private key", "Password"}).Label("Authentication")
			})
			if d.auth == "Auto" {
				ui.Text(c, "Tries ssh-agent, then the unencrypted keys in ~/.ssh.").FontSize(11).TextColor(t.TextMuted)
			}

			if d.auth == "Private key" {
				ui.Field(c, "Private key", func() {
					ui.Row(c).Gap(8).Children(func() {
						ui.TextInput(c, &d.keyPath).Font(terminalFont).FontSize(11.5).Grow(1).Label("Private key").OnSubmit(a.saveEditor)
						ui.Button(c, "Choose…").OnClick(func() { go a.chooseKey() })
					})
				}).Error(keyErr)
			}

			if d.auth == "Password" {
				ui.Field(c, "Password", func() {
					ui.TextInput(c, &d.password).Password().Label("Password").OnSubmit(a.saveEditor)
				})
				ui.Checkbox(c, &d.remember, "Remember on this device")
				if d.remember {
					ui.Text(c, "Saved in plain text in hosts.json.").FontSize(11).TextColor(t.TextMuted)
				}
			}

			ui.Row(c).Gap(8).Justify(ui.End).Padding(4, 0, 0, 0).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.editorOpen = false
				}
				if ui.PrimaryButton(c, action).Clicked() {
					a.saveEditor()
				}
			})
		})
	})
}

func (a *app) secretDialog(c *ui.Context) {
	if !a.promptOpen || a.prompt == nil {
		return
	}
	p := a.prompt
	t := c.Theme()
	h, ok := a.hostByID(p.hostID)
	if !ok {
		a.promptOpen = false
		return
	}
	title := "Password"
	if p.kind == secretPassphrase {
		title = "Key passphrase"
	}
	ui.Modal(c, &a.promptOpen, func() {
		ui.Column(c).Width(380).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(18).FontWeight(650).LetterSpacing(-0.2)
			ui.Text(c, h.userName()+"@"+h.displayAddr()).Font(terminalFont).FontSize(11.5).TextColor(t.TextMuted)
			input := ui.TextInput(c, &p.value).Password().AutoFocus().Label(title)
			if input.Submitted() {
				a.submitSecret()
			}
			if p.kind == secretPassword {
				ui.Checkbox(c, &p.remember, "Remember on this device")
			}
			if p.err != "" {
				ui.Text(c, p.err).FontSize(11.5).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(4, 0, 0, 0).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.promptOpen = false
					a.prompt = nil
				}
				label := "Connect"
				if ui.PrimaryButton(c, label).Clicked() {
					a.submitSecret()
				}
			})
		})
	})
}

func (a *app) deleteDialog(c *ui.Context) {
	if !a.confirmOpen || a.confirmID == "" {
		return
	}
	h, ok := a.hostByID(a.confirmID)
	if !ok {
		a.confirmOpen, a.confirmID = false, ""
		return
	}
	message := "This removes “" + h.displayName() + "” and closes its session."
	switch ui.AlertDialog(c, &a.confirmOpen, "Delete host?", message, "Cancel", "Delete") {
	case 1:
		a.deleteHost(a.confirmID)
		a.confirmID = ""
	case ui.AlertDismissed:
		a.confirmID = ""
	}
}

func (a *app) newHost() {
	if a.editorOpen {
		return
	}
	a.editor = newDraft()
	a.editorOpen = true
}

func (a *app) editHost(id string) {
	if a.editorOpen {
		return
	}
	h, ok := a.hostByID(id)
	if !ok {
		return
	}
	a.editor = draftOf(h)
	a.editorOpen = true
}

func (a *app) saveEditor() {
	if !a.editorOpen {
		return
	}
	d := &a.editor
	d.tried = true
	h, errs := d.validate()
	if errs != (draftErrors{}) {
		return
	}
	if !d.existing || h.ID == "" {
		h.ID = newHostID()
	}
	h = h.normalized()
	if h.Auth == authPassword {
		if d.password != "" {
			a.secrets[h.ID] = d.password
		}
		if !d.remember || d.password == "" {
			h.Password, h.Remember = "", false
		}
	} else {
		delete(a.secrets, h.ID)
		h.Password, h.Remember = "", false
	}

	if d.existing {
		a.replaceHost(h)
	} else {
		a.insertHost(h)
	}
	delete(a.failures, h.ID)
	if s := a.sessions[h.ID]; s != nil && !s.done {
		s.host = h
	}
	a.selectedID = h.ID
	a.editorOpen = false
	a.save()
	a.updateTitle()
	a.toastMsg = "Host saved"
}

func (a *app) chooseKey() {
	w := a.window()
	opts := mygo.OpenDialogOptions{
		Title:       "Choose a private key",
		DefaultPath: expandHome(a.editor.keyPath),
	}
	if w != nil {
		opts.Parent = w
	}
	paths, err := mygo.Dialog.Open(opts)
	if err != nil || len(paths) == 0 {
		return
	}
	a.post(func() {
		if a.editorOpen {
			a.editor.keyPath = paths[0]
		}
	})
}

func (a *app) confirmDelete(id string) {
	if _, ok := a.hostByID(id); !ok {
		return
	}
	a.confirmID = id
	a.confirmOpen = true
}

func (a *app) deleteHost(id string) {
	a.closeSession(id)
	a.removeHost(id)
	a.toastMsg = "Host deleted"
}

func (a *app) connectSelected() {
	if h, ok := a.selectedHost(); ok {
		a.connect(h)
	}
	a.refresh()
}

func (a *app) disconnectSelected() {
	if h, ok := a.selectedHost(); ok {
		a.disconnectHost(h.ID)
	}
}

// connect opens a session for h, asking first for a password or a key
// passphrase when the saved settings need one.
func (a *app) connect(h Host) {
	if s := a.sessions[h.ID]; s != nil {
		if !s.done {
			a.selectHost(h.ID)
			return
		}
		a.closeSession(h.ID)
	}
	if a.attempts[h.ID] != 0 {
		return
	}
	h = h.normalized()
	switch h.Auth {
	case authPassword:
		if a.secrets[h.ID] == "" && h.Password == "" {
			a.askSecret(h, secretPassword)
			return
		}
	case authKey:
		path := expandHome(h.KeyPath)
		needs, err := keyNeedsPassphrase(path)
		if err != nil {
			a.failures[h.ID] = a.failureOf(err)
			a.toastMsg = "Couldn't read the private key"
			return
		}
		if needs && a.passphrases[path] == "" {
			a.askSecret(h, secretPassphrase)
			return
		}
	}
	a.startConnect(h)
}

func (a *app) askSecret(h Host, kind secretKind) {
	a.prompt = &secretPrompt{
		hostID: h.ID,
		kind:   kind,
	}
	a.promptOpen = true
}

func (a *app) submitSecret() {
	p := a.prompt
	if p == nil || !a.promptOpen {
		return
	}
	if strings.TrimSpace(p.value) == "" {
		if p.kind == secretPassword {
			p.err = "Enter a password."
		} else {
			p.err = "Enter the passphrase."
		}
		return
	}
	h, ok := a.hostByID(p.hostID)
	if !ok {
		a.promptOpen, a.prompt = false, nil
		return
	}
	if p.kind == secretPassword {
		a.secrets[h.ID] = p.value
		if p.remember {
			h.Password = p.value
			h.Remember = true
			a.replaceHost(h)
			a.save()
		}
	} else {
		a.passphrases[expandHome(h.KeyPath)] = p.value
	}
	a.promptOpen = false
	a.prompt = nil
	a.connect(h)
}

func (a *app) startConnect(h Host) {
	password := a.secrets[h.ID]
	if password == "" {
		password = h.Password
	}
	passphrase := a.passphrases[expandHome(h.KeyPath)]

	a.nextAttempt++
	token := a.nextAttempt
	a.attempts[h.ID] = token
	delete(a.failures, h.ID)
	a.selectedID = h.ID
	a.updateTitle()
	a.save()

	go func() {
		conn, dial, err := dialSSH(h, password, passphrase)
		if err == nil {
			if loadErr := terminal.Load(); loadErr != nil {
				conn.Close()
				conn = nil
				err = fmt.Errorf("the terminal library could not start: %w", loadErr)
			}
		}
		a.post(func() {
			if a.attempts[h.ID] != token {
				if conn != nil {
					conn.Close()
				}
				return
			}
			delete(a.attempts, h.ID)
			if err != nil {
				if errors.Is(err, errBadPassphrase) {
					delete(a.passphrases, expandHome(h.KeyPath))
					a.askSecret(h, secretPassphrase)
					a.prompt.err = "That passphrase did not work. Try again."
					return
				}
				if h.Auth == authPassword && !h.Remember && isAuthFailure(err) {
					delete(a.secrets, h.ID)
					a.askSecret(h, secretPassword)
					a.prompt.err = "The server rejected that password. Try again."
					return
				}
				a.failures[h.ID] = a.failureOf(err)
				a.toastMsg = "Couldn't connect to " + h.displayName()
				return
			}
			a.startSession(h, conn, dial)
		})
	}()
}

func (a *app) startSession(h Host, conn *sshConn, dial time.Duration) {
	var s *session
	term, err := terminal.New(terminal.Options{
		Conn:         conn,
		Font:         terminal.Font{Family: terminalFont, Size: 13},
		Theme:        terminalTheme(),
		Cursor:       terminal.CursorBar,
		CopyOnSelect: terminal.CopyOnSelectClipboard,
		Padding:      terminal.Padding{X: 14, Y: 10},
		PaddingColor: terminal.PaddingExtend,
		OnExit: func(code int) {
			a.post(func() { a.sessionEnded(h.ID, s, code) })
		},
	})
	if err != nil {
		conn.Close()
		a.failures[h.ID] = a.failureOf(fmt.Errorf("the terminal could not start: %w", err))
		a.toastMsg = "Couldn't start the terminal"
		return
	}
	s = &session{host: h, conn: conn, term: term, dial: dial}
	a.sessions[h.ID] = s
	select {
	case <-term.Done():
		s.done = true
	default:
	}
	a.selectedID = h.ID
	a.termHandle.Focus()
	a.updateTitle()
	a.save()
	a.toastMsg = "Connected to " + h.displayName()
}

func (a *app) sessionEnded(id string, s *session, code int) {
	if s == nil || a.sessions[id] != s || s.done {
		return
	}
	s.done = true
	a.toastMsg = "Disconnected from " + s.host.displayName()
	a.updateTitle()
}

func (a *app) cancelConnect(id string) {
	delete(a.attempts, id)
}

func (a *app) disconnectHost(id string) {
	if a.attempts[id] != 0 {
		a.cancelConnect(id)
		return
	}
	s := a.sessions[id]
	if s == nil || s.done {
		return
	}
	// The terminal's reader ends and its OnExit marks the pane disconnected.
	s.conn.Close()
}

func (a *app) closeSession(id string) {
	s := a.sessions[id]
	if s == nil {
		return
	}
	delete(a.sessions, id)
	s.term.Close()
}

func (a *app) reconnect(h Host) {
	a.closeSession(h.ID)
	a.connect(h)
}

func (a *app) trustAndConnect(h Host) {
	f := a.failures[h.ID]
	if f == nil || f.trust == nil {
		return
	}
	if err := trustKnownHost(f.trust); err != nil {
		a.toastMsg = "Couldn't update known_hosts: " + err.Error()
		return
	}
	delete(a.failures, h.ID)
	a.connect(h)
}

func (a *app) quit() {
	a.closed.Store(true)
	a.save()
	for id, s := range a.sessions {
		delete(a.sessions, id)
		s.term.Close()
	}
}

func appDataDir() (string, error) {
	if dir, err := mygo.App.Path(mygo.PathUserData); err == nil && dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appName), nil
}
