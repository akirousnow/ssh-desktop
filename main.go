// SSH Desktop is a small SSH client in native UI: hosts in a sidebar, and
// Ghostty's terminal emulator in the window, connected over SSH with
// golang.org/x/crypto/ssh.
package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	mygo.App.SetName(appName)
	a := newApp(configPath())
	mygo.App.SetMenu(a.menu())

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     appName,
			Width:     1120,
			Height:    760,
			MinWidth:  780,
			MinHeight: 600,
			// Opens where the user left it last time.
			StateKey: "main",
			// The app is keyboard-first; the menu shows on Alt.
			AutoHideMenuBar: true,
			Content:         ui.View(a.view),
		})
		a.win.Store(win)
		win.OnClosed(a.quit)
		if a.loadErr != "" {
			a.toastMsg, a.loadErr = a.loadErr, ""
		}
		a.updateTitle()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
