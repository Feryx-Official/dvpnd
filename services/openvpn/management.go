// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// management is a client of OpenVPN's management interface. The node keeps
// one connection open for the life of the server: with management-client-auth
// every connecting client is presented to us and admitted or denied here,
// the server's status table gives per-client traffic, and a client can be
// killed. Asynchronous events (lines starting with ">") arrive interleaved
// with command replies; the reader routes replies to the commands that are
// waiting, in order, since the server answers commands sequentially.
type management struct {
	conn   net.Conn
	writer *bufio.Writer

	mu      sync.Mutex // serialises writes and the pending queue
	pending []*pendingCommand

	// onConnect decides whether a client (identified by its certificate's
	// common name) may connect; onDisconnect receives the final traffic of
	// a client's connection (cid is its client id) that ended.
	onConnect    func(commonName string) bool
	onDisconnect func(commonName, cid string, received, sent int64)

	done chan struct{}
	err  error
}

type pendingCommand struct {
	multi bool // reply ends with END rather than a single SUCCESS/ERROR line
	lines []string
	done  chan struct{}
}

// dialManagement connects to the management socket, retrying until the server
// has opened it or the deadline passes.
func dialManagement(socket string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("management interface at %s: %w", socket, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func newManagement(conn net.Conn, onConnect func(string) bool, onDisconnect func(string, string, int64, int64)) *management {
	m := &management{
		conn:         conn,
		writer:       bufio.NewWriter(conn),
		onConnect:    onConnect,
		onDisconnect: onDisconnect,
		done:         make(chan struct{}),
	}
	go m.read()

	return m
}

func (m *management) Close() error {
	err := m.conn.Close()
	<-m.done

	return err
}

// run sends a command and waits for its reply.
func (m *management) run(command string, multi bool, timeout time.Duration) ([]string, error) {
	p := &pendingCommand{multi: multi, done: make(chan struct{})}

	m.mu.Lock()
	m.pending = append(m.pending, p)
	_, err := m.writer.WriteString(command + "\n")
	if err == nil {
		err = m.writer.Flush()
	}
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case <-p.done:
	case <-m.done:
		return nil, fmt.Errorf("management connection closed: %v", m.err)
	case <-time.After(timeout):
		return nil, fmt.Errorf("management command %q timed out", command)
	}

	if len(p.lines) > 0 && strings.HasPrefix(p.lines[len(p.lines)-1], "ERROR:") {
		return p.lines, errors.New(p.lines[len(p.lines)-1])
	}

	return p.lines, nil
}

// send writes a command whose reply is not needed beyond consuming it.
func (m *management) send(command string) {
	go func() { _, _ = m.run(command, false, 10*time.Second) }()
}

func (m *management) read() {
	defer close(m.done)

	scanner := bufio.NewScanner(m.conn)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var event *clientEvent
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, ">") {
			event = m.handleEvent(event, line)

			continue
		}

		m.mu.Lock()
		if len(m.pending) == 0 {
			m.mu.Unlock()

			continue
		}
		p := m.pending[0]
		p.lines = append(p.lines, line)
		finished := strings.HasPrefix(line, "SUCCESS:") || strings.HasPrefix(line, "ERROR:") || (p.multi && line == "END")
		if finished {
			m.pending = m.pending[1:]
		}
		m.mu.Unlock()
		if finished {
			close(p.done)
		}
	}
	m.err = scanner.Err()
}

// clientEvent is a >CLIENT: notification with its environment block.
type clientEvent struct {
	kind string // CONNECT, REAUTH, ESTABLISHED, DISCONNECT
	cid  string
	kid  string
	env  map[string]string
}

// handleEvent consumes one ">" line. CONNECT, REAUTH, ESTABLISHED and
// DISCONNECT are followed by ">CLIENT:ENV,name=value" lines up to
// ">CLIENT:ENV,END"; the decision is taken when the block is complete.
func (m *management) handleEvent(current *clientEvent, line string) *clientEvent {
	if !strings.HasPrefix(line, ">CLIENT:") {
		return current
	}

	body := strings.TrimPrefix(line, ">CLIENT:")
	kind, rest, _ := strings.Cut(body, ",")

	switch kind {
	case "CONNECT", "REAUTH", "ESTABLISHED", "DISCONNECT":
		ev := &clientEvent{kind: kind, env: map[string]string{}}
		ev.cid, ev.kid, _ = strings.Cut(rest, ",")

		return ev
	case "ENV":
		if current == nil {
			return nil
		}
		if rest == "END" {
			m.finishEvent(current)

			return nil
		}
		if k, v, ok := strings.Cut(rest, "="); ok {
			current.env[k] = v
		}

		return current
	default:
		return current
	}
}

func (m *management) finishEvent(ev *clientEvent) {
	cn := ev.env["common_name"]
	if cn == "" {
		cn = ev.env["X509_0_CN"]
	}

	switch ev.kind {
	case "CONNECT", "REAUTH":
		if m.onConnect != nil && m.onConnect(cn) {
			m.send(fmt.Sprintf("client-auth-nt %s %s", ev.cid, ev.kid))
		} else {
			m.send(fmt.Sprintf("client-deny %s %s \"not a registered peer\"", ev.cid, ev.kid))
		}
	case "DISCONNECT":
		if m.onDisconnect != nil {
			received, _ := strconv.ParseInt(ev.env["bytes_received"], 10, 64)
			sent, _ := strconv.ParseInt(ev.env["bytes_sent"], 10, 64)
			m.onDisconnect(cn, ev.cid, received, sent)
		}
	}
}

// clientStatus is one row of "status 3".
type clientStatus struct {
	commonName string
	received   int64 // bytes the server received from the client: its upload
	sent       int64 // bytes the server sent to the client: its download
	cid        string
}

// status reads the connected clients.
func (m *management) status() ([]clientStatus, error) {
	lines, err := m.run("status 3", true, 10*time.Second)
	if err != nil {
		return nil, err
	}

	var rows []clientStatus
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 11 || fields[0] != "CLIENT_LIST" {
			continue
		}
		received, _ := strconv.ParseInt(fields[5], 10, 64)
		sent, _ := strconv.ParseInt(fields[6], 10, 64)
		rows = append(rows, clientStatus{commonName: fields[1], received: received, sent: sent, cid: fields[10]})
	}

	return rows, nil
}

// kill disconnects every connection of this common name. client-kill by
// client id tells the client to restart at once (a plain kill leaves it to
// notice at its keepalive timeout); a name without a connection is not an
// error.
func (m *management) kill(commonName string) error {
	rows, err := m.status()
	if err != nil {
		return err
	}

	for _, row := range rows {
		if row.commonName != commonName {
			continue
		}
		if _, err := m.run(fmt.Sprintf("client-kill %s RESTART,session-ended", row.cid), false, 10*time.Second); err != nil &&
			!strings.Contains(err.Error(), "not found") {
			return err
		}
	}

	return nil
}
