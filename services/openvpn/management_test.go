// SPDX-License-Identifier: Apache-2.0

package openvpn

import (
	"bufio"
	"encoding/base64"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// fakeServer imitates OpenVPN's management interface: it records the
// commands it receives, answers status and kill, and can inject events.
type fakeServer struct {
	lis      net.Listener
	mu       sync.Mutex
	conn     net.Conn
	commands []string
	clients  []clientStatus
	failKill bool
	ready    chan struct{}
}

func startFakeServer(t *testing.T) *fakeServer {
	return startFakeServerOn(t, filepath.Join(t.TempDir(), "openvpn.sock"))
}

func startFakeServerOn(t *testing.T, socket string) *fakeServer {
	t.Helper()

	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{lis: lis, ready: make(chan struct{})}
	go f.serve()
	t.Cleanup(func() { lis.Close() })

	return f
}

func (f *fakeServer) socket() string {
	return f.lis.Addr().String()
}

func (f *fakeServer) serve() {
	conn, err := f.lis.Accept()
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	close(f.ready)

	_, _ = conn.Write([]byte(">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info\n"))

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		cmd := scanner.Text()
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		clients := append([]clientStatus(nil), f.clients...)
		failKill := f.failKill
		f.mu.Unlock()

		switch {
		case cmd == "status 3":
			var b strings.Builder
			b.WriteString("TITLE\tOpenVPN 2.6.14\nTIME\t2026-09-08 12:00:00\t1789000000\n")
			b.WriteString("HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\tPeer ID\tData Channel Cipher\n")
			for _, c := range clients {
				b.WriteString("CLIENT_LIST\t" + c.commonName + "\t203.0.113.5:4000\t10.9.0.2\t\t" +
					itoa(c.received) + "\t" + itoa(c.sent) + "\t2026-09-08 11:59:00\t1788999940\tUNDEF\t" + c.cid + "\t0\tAES-256-GCM\n")
			}
			b.WriteString("GLOBAL_STATS\tMax bcast/mcast queue length\t0\nEND\n")
			_, _ = conn.Write([]byte(b.String()))
		case strings.HasPrefix(cmd, "client-kill ") && failKill:
			_, _ = conn.Write([]byte("ERROR: client-kill command failed\n"))
		case strings.HasPrefix(cmd, "client-kill "):
			cid := strings.Fields(cmd)[1]
			found := false
			for _, c := range clients {
				if c.cid == cid {
					found = true
				}
			}
			if found {
				_, _ = conn.Write([]byte("SUCCESS: client-kill command succeeded\n"))
			} else {
				_, _ = conn.Write([]byte("ERROR: client-kill: client ID " + cid + " not found\n"))
			}
		case strings.HasPrefix(cmd, "client-auth-nt "), strings.HasPrefix(cmd, "client-deny "):
			_, _ = conn.Write([]byte("SUCCESS: client-auth command succeeded\n"))
		default:
			_, _ = conn.Write([]byte("ERROR: unknown command\n"))
		}
	}
}

// event writes an asynchronous notification with an environment block.
func (f *fakeServer) event(kind, cid, kid string, env map[string]string) {
	<-f.ready
	var b strings.Builder
	b.WriteString(">CLIENT:" + kind + "," + cid)
	if kid != "" {
		b.WriteString("," + kid)
	}
	b.WriteString("\n")
	for k, v := range env {
		b.WriteString(">CLIENT:ENV," + k + "=" + v + "\n")
	}
	b.WriteString(">CLIENT:ENV,END\n")
	f.mu.Lock()
	_, _ = f.conn.Write([]byte(b.String()))
	f.mu.Unlock()
}

func (f *fakeServer) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.commands...)
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Rules: [SL-16].
func TestManagementAdmitStatusKill(t *testing.T) {
	f := startFakeServer(t)
	f.clients = []clientStatus{{commonName: "peer-a", received: 100, sent: 2000, cid: "7"}}

	conn, err := dialManagement(f.socket(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// The disconnect callback runs on the management reader's goroutine.
	var (
		mu           sync.Mutex
		disconnected []string
	)
	gone := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), disconnected...)
	}
	m := newManagement(conn,
		func(cn string) bool { return cn == "peer-a" },
		func(cn, cid string, rx, tx int64) {
			mu.Lock()
			defer mu.Unlock()
			disconnected = append(disconnected, cn+"#"+cid+":"+itoa(rx)+"/"+itoa(tx))
		})
	t.Cleanup(func() { m.Close() })

	rows, err := m.status()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].commonName != "peer-a" || rows[0].received != 100 || rows[0].sent != 2000 || rows[0].cid != "7" {
		t.Fatalf("status: %+v", rows)
	}

	// A registered peer is admitted, an unknown one denied; the replies are
	// commands too and must not confuse a status query issued meanwhile.
	f.event("CONNECT", "7", "1", map[string]string{"common_name": "peer-a", "untrusted_ip": "203.0.113.5"})
	f.event("CONNECT", "8", "1", map[string]string{"common_name": "stranger"})
	if _, err := m.status(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "auth replies", func() bool {
		cmds := strings.Join(f.received(), "\n")
		return strings.Contains(cmds, "client-auth-nt 7 1") && strings.Contains(cmds, "client-deny 8 1")
	})

	f.event("DISCONNECT", "7", "", map[string]string{"common_name": "peer-a", "bytes_received": "150", "bytes_sent": "2500"})
	waitFor(t, "disconnect", func() bool { return len(gone()) == 1 })
	if got := gone(); got[0] != "peer-a#7:150/2500" {
		t.Fatalf("disconnect: %v", got)
	}

	if err := m.kill("peer-a"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := m.kill("nobody"); err != nil {
		t.Fatalf("kill of an unknown name must not fail: %v", err)
	}
	if cmds := strings.Join(f.received(), "\n"); !strings.Contains(cmds, `client-kill 7 RESTART,session-ended`) {
		t.Fatalf("client-kill not sent by id: %s", cmds)
	}
	if _, err := m.run("bogus", false, time.Second); err == nil {
		t.Fatal("ERROR reply must surface")
	}
}
