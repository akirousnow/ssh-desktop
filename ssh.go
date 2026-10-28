package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

var errPassphraseNeeded = errors.New("the private key is encrypted")

// unknownHostError is a host whose key is not in known_hosts yet. It is
// shown as a trust prompt rather than a failure.
type unknownHostError struct {
	hostName    string
	display     string
	fingerprint string
	key         ssh.PublicKey
}

func (e *unknownHostError) Error() string { return "host key is not known" }

// hostKeyChangedError is a key that differs from the one on record.
type hostKeyChangedError struct {
	display          string
	known, presented string
}

func (e *hostKeyChangedError) Error() string { return "host key has changed" }

// sshConn is a terminal's Conn: it reads the session's output and writes
// the keyboard to its input.
type sshConn struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	pr      *io.PipeReader
	closers []io.Closer

	once sync.Once
	stop chan struct{}
}

func dialSSH(h Host, password, passphrase string) (*sshConn, time.Duration, error) {
	start := time.Now()
	methods, closers, err := authMethods(h, password, passphrase)
	if err != nil {
		return nil, 0, err
	}
	config := &ssh.ClientConfig{
		User:            h.userName(),
		Auth:            methods,
		HostKeyCallback: hostKeyCallback(h),
		Timeout:         15 * time.Second,
		// x/crypto prefers ECDSA over ED25519 by default, which does not
		// match the key OpenSSH writes to known_hosts for most servers.
		// Prefer the key type users are most likely to have on record.
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519,
			ssh.KeyAlgoECDSA256,
			ssh.KeyAlgoECDSA384,
			ssh.KeyAlgoECDSA521,
			ssh.KeyAlgoRSASHA512,
			ssh.KeyAlgoRSASHA256,
			ssh.KeyAlgoRSA,
		},
	}
	client, err := ssh.Dial("tcp", h.dialAddr(), config)
	if err != nil {
		closeAll(closers)
		return nil, 0, err
	}
	session, err := client.NewSession()
	if err != nil {
		client.Close()
		closeAll(closers)
		return nil, 0, err
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		session.Close()
		client.Close()
		closeAll(closers)
		return nil, 0, fmt.Errorf("the server refused a terminal: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		closeAll(closers)
		return nil, 0, err
	}
	pr, pw := io.Pipe()
	session.Stdout = pw
	session.Stderr = pw
	if err := session.Shell(); err != nil {
		pr.Close()
		pw.Close()
		session.Close()
		client.Close()
		closeAll(closers)
		return nil, 0, fmt.Errorf("the server refused a shell: %w", err)
	}
	conn := &sshConn{
		client:  client,
		session: session,
		stdin:   stdin,
		pr:      pr,
		closers: closers,
		stop:    make(chan struct{}),
	}
	go func() {
		// The terminal drains what the session wrote, then sees EOF; its
		// Close hangs up the rest.
		_ = session.Wait()
		pw.Close()
	}()
	go conn.keepalive()
	return conn, time.Since(start), nil
}

func (c *sshConn) keepalive() {
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			// No reply wanted: a server free to ignore it stays connected,
			// while the write keeps the path warm.
			_, _, _ = c.client.SendRequest("keepalive@openssh.com", false, nil)
		}
	}
}

func (c *sshConn) Read(p []byte) (int, error) { return c.pr.Read(p) }

func (c *sshConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

// Resize tells the remote pseudo-terminal its new size.
func (c *sshConn) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return c.session.WindowChange(rows, cols)
}

func (c *sshConn) Close() error {
	c.once.Do(func() {
		close(c.stop)
		c.pr.CloseWithError(io.EOF)
		c.stdin.Close()
		c.session.Close()
		c.client.Close()
		closeAll(c.closers)
	})
	return nil
}

func authMethods(h Host, password, passphrase string) ([]ssh.AuthMethod, []io.Closer, error) {
	switch h.Auth {
	case authAuto:
		return autoAuthMethods()
	case authPassword:
		if password == "" {
			return nil, nil, errPasswordNeeded
		}
		answers := func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			out := make([]string, len(questions))
			for i := range out {
				out[i] = password
			}
			return out, nil
		}
		return []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(answers),
		}, nil, nil
	case authKey:
		path := expandHome(h.KeyPath)
		if path == "" {
			return nil, nil, errors.New("no private key is selected")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("could not read %s: %w", shortPath(path), err)
		}
		var signer ssh.Signer
		if passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(b, []byte(passphrase))
			if err != nil && strings.Contains(err.Error(), "decryption password incorrect") {
				return nil, nil, errBadPassphrase
			}
		} else {
			signer, err = ssh.ParsePrivateKey(b)
		}
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				return nil, nil, errPassphraseNeeded
			}
			return nil, nil, fmt.Errorf("could not read %s: %w", shortPath(path), err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil, nil
	default:
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, nil, errors.New("no SSH agent is running (SSH_AUTH_SOCK is not set)")
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, nil, fmt.Errorf("could not reach the SSH agent: %w", err)
		}
		keys, err := agent.NewClient(conn).Signers()
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("could not read the SSH agent's keys: %w", err)
		}
		if len(keys) == 0 {
			conn.Close()
			return nil, nil, errors.New("the SSH agent has no keys; add one with ssh-add")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(keys...)}, []io.Closer{conn}, nil
	}
}

// autoAuthMethods offers the SSH agent's keys, then every key in ~/.ssh
// that needs no passphrase. Passphrase-protected keys are left to the
// Private key method, which can ask for their passphrase.
func autoAuthMethods() ([]ssh.AuthMethod, []io.Closer, error) {
	var methods []ssh.AuthMethod
	var closers []io.Closer
	seen := map[string]bool{}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			keys, err := agent.NewClient(conn).Signers()
			if err == nil && len(keys) > 0 {
				methods = append(methods, ssh.PublicKeys(keys...))
				closers = append(closers, conn)
				for _, key := range keys {
					seen[ssh.FingerprintSHA256(key.PublicKey())] = true
				}
			} else {
				conn.Close()
			}
		}
	}

	encrypted := 0
	for _, path := range autoKeyFiles() {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				encrypted++
			}
			continue
		}
		fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if len(methods) == 0 {
		if encrypted > 0 {
			return nil, nil, errors.New("auto found only encrypted keys in ~/.ssh; choose Private key to enter the passphrase")
		}
		return nil, nil, errors.New("auto found no keys: start ssh-agent or put a key in ~/.ssh")
	}
	return methods, closers, nil
}

// autoKeyFiles lists the files in ~/.ssh likely to be private keys, the
// usual names first. Files that do not parse are skipped by the caller.
func autoKeyFiles() []string {
	dir := filepath.Join(homeDir, ".ssh")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	rank := func(name string) int {
		switch name {
		case "id_ed25519":
			return 0
		case "id_ecdsa":
			return 1
		case "id_rsa":
			return 2
		case "id_dsa":
			return 3
		}
		return 10
	}
	type candidate struct {
		name string
		rank int
	}
	var candidates []candidate
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".pub") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.IsDir() || info.Size() == 0 || info.Size() > 1<<20 {
			continue
		}
		candidates = append(candidates, candidate{name: name, rank: rank(name)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].name < candidates[j].name
	})
	paths := make([]string, len(candidates))
	for i, c := range candidates {
		paths[i] = filepath.Join(dir, c.name)
	}
	return paths
}

func closeAll(closers []io.Closer) {
	for _, c := range closers {
		c.Close()
	}
}

func keyNeedsPassphrase(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("could not read %s: %w", shortPath(path), err)
	}
	if _, err := ssh.ParsePrivateKey(b); err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return true, nil
		}
		return false, fmt.Errorf("could not read %s: %w", shortPath(path), err)
	}
	return false, nil
}

func hostKeyCallback(h Host) ssh.HostKeyCallback {
	path := knownHostsPath()
	check, _ := knownhosts.New(path)
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if check == nil {
			return &unknownHostError{
				hostName:    knownhosts.Normalize(hostname),
				display:     h.displayAddr(),
				fingerprint: ssh.FingerprintSHA256(key),
				key:         key,
			}
		}
		err := check(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) == 0 {
				return &unknownHostError{
					hostName:    knownhosts.Normalize(hostname),
					display:     h.displayAddr(),
					fingerprint: ssh.FingerprintSHA256(key),
					key:         key,
				}
			}
			// A host known only by another key type is not a changed key:
			// ssh-keygen writes the type it negotiated, while another client
			// may negotiate another. Offer to trust this key rather than
			// crying man-in-the-middle.
			for _, want := range keyErr.Want {
				if want.Key.Type() == key.Type() {
					return &hostKeyChangedError{
						display:   h.displayAddr(),
						known:     ssh.FingerprintSHA256(want.Key),
						presented: ssh.FingerprintSHA256(key),
					}
				}
			}
			return &unknownHostError{
				hostName:    knownhosts.Normalize(hostname),
				display:     h.displayAddr(),
				fingerprint: ssh.FingerprintSHA256(key),
				key:         key,
			}
		}
		return err
	}
}

func trustKnownHost(e *unknownHostError) error {
	path := knownHostsPath()
	if path == "" {
		return errors.New("no home directory for known_hosts")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line := knownhosts.Line([]string{e.hostName}, e.key)
	_, err = f.WriteString(line + "\n")
	return err
}

// connFailure is what the host pane shows about a failed attempt.
type connFailure struct {
	title   string
	message string
	detail  string
	trust   *unknownHostError
}

func (a *app) failureOf(err error) *connFailure {
	var unknown *unknownHostError
	if errors.As(err, &unknown) {
		return &connFailure{
			title:   "Unrecognized host",
			message: "The key presented by " + unknown.display + " is not in known_hosts yet. Check the fingerprint before trusting it.",
			detail:  "Fingerprint: " + unknown.fingerprint,
			trust:   unknown,
		}
	}
	var changed *hostKeyChangedError
	if errors.As(err, &changed) {
		return &connFailure{
			title:   "Host key changed",
			message: "The key presented by " + changed.display + " does not match the one on record. The server may have been reinstalled, or the connection may be intercepted.",
			detail:  "Known key:    " + changed.known + "\nPresented key: " + changed.presented + "\n\nIf you trust the change, remove the old entry from ~/.ssh/known_hosts and try again.",
		}
	}
	if errors.Is(err, errPassphraseNeeded) {
		return &connFailure{
			title:   "Passphrase needed",
			message: "The private key is encrypted. Its passphrase is needed to unlock it.",
			detail:  err.Error(),
		}
	}
	if errors.Is(err, errPasswordNeeded) {
		return &connFailure{
			title:   "Password needed",
			message: "The server needs a password.",
			detail:  err.Error(),
		}
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "unable to authenticate"):
		return &connFailure{
			title:   "Authentication failed",
			message: "The server rejected the credentials. Check the user name, key or password.",
			detail:  msg,
		}
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "server misbehaving"):
		return &connFailure{
			title:   "Host not found",
			message: "No address is known for this server. Check the host name.",
			detail:  msg,
		}
	case strings.Contains(lower, "connection refused"):
		return &connFailure{
			title:   "Connection refused",
			message: "Nothing is accepting SSH connections there. Check the port.",
			detail:  msg,
		}
	case strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "timeout"):
		return &connFailure{
			title:   "Connection timed out",
			message: "The server did not answer. Check the address and your network.",
			detail:  msg,
		}
	case strings.Contains(lower, "auto found only encrypted keys"):
		return &connFailure{
			title:   "The key is encrypted",
			message: "Auto found a key in ~/.ssh, but it needs a passphrase. Choose Private key as the method to enter it.",
			detail:  msg,
		}
	case strings.Contains(lower, "auto found no keys"):
		return &connFailure{
			title:   "No key found",
			message: "Auto looked for ssh-agent and for unencrypted keys in ~/.ssh, and found none. Add a key, or choose another method.",
			detail:  msg,
		}
	case strings.Contains(lower, "no ssh agent"):
		return &connFailure{
			title:   "No SSH agent",
			message: "Start ssh-agent and add a key, or switch this host to a private key or password.",
			detail:  msg,
		}
	case strings.Contains(lower, "private key") || strings.Contains(lower, "passphrase"):
		return &connFailure{
			title:   "Couldn't use the key",
			message: "The private key could not be read.",
			detail:  msg,
		}
	}
	return &connFailure{
		title:   "Couldn't connect",
		message: "The connection could not be established.",
		detail:  msg,
	}
}

var (
	errPasswordNeeded = errors.New("a password is needed")
	errBadPassphrase  = errors.New("the key's passphrase is not correct")
)

// isAuthFailure reports the server refusing every method offered.
func isAuthFailure(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unable to authenticate")
}
