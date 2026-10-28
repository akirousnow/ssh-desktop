package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	authAuto     = "auto"
	authAgent    = "agent"
	authKey      = "key"
	authPassword = "password"
)

// Host is a saved server. The JSON is the file format of hosts.json, so
// fields keep their names.
type Host struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Auth     string `json:"auth"`
	KeyPath  string `json:"keyPath,omitempty"`
	Password string `json:"password,omitempty"`
	Remember bool   `json:"remember,omitempty"`
}

type configFile struct {
	Hosts    []Host `json:"hosts"`
	LastHost string `json:"lastHost,omitempty"`
}

func (h Host) port() int {
	if h.Port <= 0 || h.Port > 65535 {
		return 22
	}
	return h.Port
}

func (h Host) userName() string {
	if u := strings.TrimSpace(h.User); u != "" {
		return u
	}
	return loginUser()
}

func (h Host) displayName() string {
	if n := strings.TrimSpace(h.Name); n != "" {
		return n
	}
	return h.userName() + "@" + h.displayAddr()
}

func (h Host) displayAddr() string {
	if h.port() == 22 {
		return h.Address
	}
	return net.JoinHostPort(h.Address, strconv.Itoa(h.port()))
}

func (h Host) dialAddr() string {
	return net.JoinHostPort(h.Address, strconv.Itoa(h.port()))
}

func (h Host) authLabel() string {
	switch h.Auth {
	case authKey:
		return "Private key"
	case authPassword:
		return "Password"
	case authAgent:
		return "SSH agent"
	default:
		return "Auto"
	}
}

func (h Host) normalized() Host {
	switch h.Auth {
	case authAuto, authAgent, authKey, authPassword:
	default:
		h.Auth = authAuto
	}
	if h.Port <= 0 || h.Port > 65535 {
		h.Port = 22
	}
	h.Address = strings.TrimSpace(h.Address)
	h.User = strings.TrimSpace(h.User)
	h.Name = strings.TrimSpace(h.Name)
	if h.ID == "" {
		h.ID = newHostID()
	}
	return h
}

type draftErrors struct {
	address, user, port, keyPath string
}

// hostDraft is the form of the host editor, with text fields for the
// numbers so that typing stays unconstrained.
type hostDraft struct {
	existing bool
	id       string
	name     string
	address  string
	user     string
	port     string
	auth     string
	keyPath  string
	password string
	remember bool
	tried    bool
}

func newDraft() hostDraft {
	return hostDraft{
		user:    loginUser(),
		port:    "22",
		auth:    authLabelOf(authAuto),
		keyPath: defaultKeyPath(),
	}
}

func draftOf(h Host) hostDraft {
	return hostDraft{
		existing: true,
		id:       h.ID,
		name:     h.Name,
		address:  h.Address,
		user:     h.User,
		port:     strconv.Itoa(h.port()),
		auth:     authLabelOf(h.normalized().Auth),
		keyPath:  h.KeyPath,
		password: h.Password,
		remember: h.Remember,
	}
}

// validate returns the parsed host and the fields that need attention.
func (d *hostDraft) validate() (Host, draftErrors) {
	var errs draftErrors
	address := strings.TrimSpace(d.address)
	address = strings.TrimPrefix(address, "ssh://")
	if i := strings.IndexByte(address, '/'); i >= 0 {
		address = address[:i]
	}
	parsedUser, parsedHost, parsedPort := splitAddress(address)
	userName := strings.TrimSpace(d.user)
	if parsedUser != "" {
		userName = parsedUser
	}
	port := strings.TrimSpace(d.port)
	if parsedPort != 0 {
		port = strconv.Itoa(parsedPort)
	}
	if parsedHost != "" {
		address = parsedHost
	}
	auth := authKindOf(d.auth)

	if address == "" {
		errs.address = "Enter a host name or IP address."
	}
	if userName == "" {
		errs.user = "Enter a user name."
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		errs.port = "1–65535"
	}
	keyPath := strings.TrimSpace(d.keyPath)
	if auth == authKey && keyPath == "" {
		errs.keyPath = "Choose a private key."
	}
	name := strings.TrimSpace(d.name)
	if name == "" && address != "" && userName != "" {
		name = userName + "@" + address
	}

	host := Host{
		ID:      d.id,
		Name:    name,
		Address: address,
		Port:    portNumber,
		User:    userName,
		Auth:    auth,
		KeyPath: keyPath,
	}
	if auth == authPassword && d.remember {
		host.Password = d.password
		host.Remember = true
	}
	return host, errs
}

func authLabelOf(kind string) string {
	switch kind {
	case authKey:
		return "Private key"
	case authPassword:
		return "Password"
	case authAgent:
		return "SSH agent"
	default:
		return "Auto"
	}
}

func authKindOf(label string) string {
	switch label {
	case "Private key":
		return authKey
	case "Password":
		return authPassword
	case "SSH agent":
		return authAgent
	default:
		return authAuto
	}
}

// splitAddress accepts "host", "host:port", "user@host" and
// "user@host:port", with brackets around IPv6 addresses.
func splitAddress(s string) (userName, host string, port int) {
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		userName, s = s[:i], s[i+1:]
	}
	if strings.HasPrefix(s, "[") {
		if end := strings.IndexByte(s, ']'); end >= 0 {
			host = s[1:end]
			rest := s[end+1:]
			if strings.HasPrefix(rest, ":") {
				port, _ = strconv.Atoi(rest[1:])
			}
			return userName, host, port
		}
	}
	if strings.Count(s, ":") == 1 {
		if h, p, err := net.SplitHostPort(s); err == nil {
			n, err := strconv.Atoi(p)
			if err == nil {
				return userName, h, n
			}
		}
	}
	return userName, s, 0
}

func newHostID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

var homeDir, _ = os.UserHomeDir()

func defaultKeyPath() string {
	if homeDir == "" {
		return ""
	}
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		path := filepath.Join(homeDir, ".ssh", name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join(homeDir, ".ssh", "id_ed25519")
}

func knownHostsPath() string {
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, ".ssh", "known_hosts")
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "~":
		return homeDir
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

func shortPath(path string) string {
	if homeDir != "" && strings.HasPrefix(path, homeDir+string(os.PathSeparator)) {
		return "~/" + filepath.ToSlash(strings.TrimPrefix(path, homeDir+string(os.PathSeparator)))
	}
	return path
}

func loginUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		if i := strings.LastIndexByte(name, '\\'); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	if name := os.Getenv("USERNAME"); name != "" {
		return name
	}
	return "root"
}

func configPath() string {
	dir, err := appDataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "hosts.json")
}

func (a *app) load() {
	if a.configPath == "" {
		return
	}
	b, err := os.ReadFile(a.configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			a.loadErr = "Couldn't read hosts.json: " + err.Error()
		}
		return
	}
	var cfg configFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		a.loadErr = "hosts.json is damaged: " + err.Error()
		return
	}
	for _, h := range cfg.Hosts {
		if strings.TrimSpace(h.Address) == "" {
			continue
		}
		a.hosts = append(a.hosts, h.normalized())
	}
	if h, ok := a.hostByID(cfg.LastHost); ok {
		a.selectedID = h.ID
	} else if len(a.hosts) > 0 {
		a.selectedID = a.hosts[0].ID
	}
}

func (a *app) save() {
	if a.configPath == "" {
		return
	}
	cfg := configFile{Hosts: a.hosts, LastHost: a.selectedID}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(a.configPath), 0o700)
	}
	if err == nil {
		tmp := a.configPath + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, a.configPath)
		}
	}
	if err != nil {
		a.toastMsg = "Couldn't save hosts: " + err.Error()
	}
}

func (a *app) hostByID(id string) (Host, bool) {
	for _, h := range a.hosts {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}

func (a *app) selectedHost() (Host, bool) {
	return a.hostByID(a.selectedID)
}

func (a *app) selectHost(id string) {
	if a.selectedID == id {
		return
	}
	if _, ok := a.hostByID(id); !ok {
		return
	}
	a.selectedID = id
	a.updateTitle()
	a.save()
}

func (a *app) filteredHosts() []Host {
	q := strings.ToLower(strings.TrimSpace(a.query))
	if q == "" {
		return a.hosts
	}
	out := make([]Host, 0, len(a.hosts))
	for _, h := range a.hosts {
		hay := strings.ToLower(h.displayName() + " " + h.Address + " " + h.userName())
		if strings.Contains(hay, q) {
			out = append(out, h)
		}
	}
	return out
}

func (a *app) removeHost(id string) {
	for i, h := range a.hosts {
		if h.ID == id {
			a.hosts = append(a.hosts[:i], a.hosts[i+1:]...)
			break
		}
	}
	delete(a.failures, id)
	delete(a.secrets, id)
	delete(a.attempts, id)
	if a.selectedID == id {
		a.selectedID = ""
		if len(a.hosts) > 0 {
			a.selectedID = a.hosts[0].ID
		}
	}
	a.updateTitle()
	a.save()
}

func (a *app) insertHost(h Host) {
	h = h.normalized()
	a.hosts = append([]Host{h}, a.hosts...)
	a.selectedID = h.ID
}

func (a *app) replaceHost(h Host) {
	for i := range a.hosts {
		if a.hosts[i].ID == h.ID {
			a.hosts[i] = h
			return
		}
	}
	a.insertHost(h)
}

func (a *app) duplicateHost(id string) {
	h, ok := a.hostByID(id)
	if !ok {
		return
	}
	h.ID = newHostID()
	h.Name = h.displayName() + " copy"
	at := len(a.hosts)
	for i := range a.hosts {
		if a.hosts[i].ID == id {
			at = i + 1
			break
		}
	}
	a.hosts = append(a.hosts, Host{})
	copy(a.hosts[at+1:], a.hosts[at:])
	a.hosts[at] = h
	a.selectedID = h.ID
	a.save()
	a.updateTitle()
	a.toastMsg = "Host duplicated"
}
