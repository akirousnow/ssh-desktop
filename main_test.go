package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func testApp(t *testing.T) *app {
	t.Helper()
	a := newApp(filepath.Join(t.TempDir(), "hosts.json"))
	a.hosts = []Host{
		{ID: "a", Name: "Mac mini", Address: "192.168.1.12", Port: 22, User: "yee", Auth: authAgent},
		{ID: "b", Name: "Production", Address: "prod.example.com", Port: 2222, User: "deploy", Auth: authKey, KeyPath: "~/.ssh/id_ed25519"},
	}
	a.selectedID = "a"
	return a
}

func TestSearchFiltersHosts(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	if !tt.HasText("Mac mini") || !tt.HasText("Production") {
		t.Fatalf("hosts missing: %q", tt.Texts())
	}
	if err := tt.Click("Search hosts"); err != nil {
		t.Fatal(err)
	}
	tt.Type("nothing-matches-this")
	if !tt.HasText("No hosts match.") {
		t.Errorf("empty filter message missing: %q", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("prod")
	if got := a.filteredHosts(); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("filtered hosts %+v, want only Production", got)
	}
}

func TestSplitAddress(t *testing.T) {
	cases := []struct {
		in   string
		user string
		host string
		port int
	}{
		{"example.com", "", "example.com", 0},
		{"user@example.com", "user", "example.com", 0},
		{"example.com:2222", "", "example.com", 2222},
		{"user@example.com:2222", "user", "example.com", 2222},
		{"[::1]:2222", "", "::1", 2222},
	}
	for _, c := range cases {
		user, host, port := splitAddress(c.in)
		if user != c.user || host != c.host || port != c.port {
			t.Errorf("splitAddress(%q) = %q, %q, %d; want %q, %q, %d", c.in, user, host, port, c.user, c.host, c.port)
		}
	}
}

func TestChoosingHostShowsItsDetails(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	if err := tt.Click("Production"); err != nil {
		t.Fatal(err)
	}
	if a.selectedID != "b" {
		t.Fatalf("selected %q, want b", a.selectedID)
	}
	if !tt.HasText("deploy@prod.example.com:2222") {
		t.Errorf("details missing: %q", tt.Texts())
	}
}

func TestNewHostDialogSaves(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	if err := tt.Click("New host"); err != nil {
		t.Fatal(err)
	}
	// The name field has the focus; fill the form by labels.
	if err := tt.Click("Host"); err != nil {
		t.Fatal(err)
	}
	tt.Type("root@10.0.0.5:2200")
	if err := tt.Click("Auto"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Password"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Add host"); err != nil {
		t.Fatal(err)
	}
	if len(a.hosts) != 3 {
		t.Fatalf("%d hosts after save, want 3: %+v", len(a.hosts), a.hosts)
	}
	h := a.hosts[0]
	if h.Address != "10.0.0.5" || h.User != "root" || h.port() != 2200 || h.Auth != authPassword {
		t.Errorf("saved host %+v", h)
	}
}
