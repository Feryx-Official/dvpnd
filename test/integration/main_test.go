// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package integration runs the node's services with their real daemons and
// real clients and checks, protocol by protocol, what a client can and cannot
// reach through the node. test/integration/run.sh builds the containers that
// stand in for a host, the internet and the provider's private network, and
// runs this package in them; anywhere else every test is skipped.
//
// The test binary has two more roles, picked by DVPND_IT_ROLE: "target", the
// host on the internet and on the private network that clients try to reach,
// and "dial", which dials the targets given as arguments and prints what it
// reached (run inside a client's network namespace, or as the proxy account).
package integration

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/types"
)

// stopWatched stops svc after checking how its daemon was watched: Done
// stays open while the real daemon serves (the start command stops the node
// on it, so a false alarm would restart a healthy node again and again), and
// it is closed once Stop returns.
func stopWatched(t *testing.T, svc types.Service) error {
	t.Helper()
	d, watched := svc.(types.Supervised)
	if watched {
		select {
		case <-d.Done():
			t.Error("Done is closed while the daemon serves: the start command would stop a healthy node")
		default:
		}
	}
	err := svc.Stop()
	if watched {
		select {
		case <-d.Done():
		case <-time.After(5 * time.Second):
			t.Error("Done is still open 5 s after Stop")
		}
	}

	return err
}

// Ports the targets and the host's own services listen on.
const (
	targetPort  = 18080 // the target's service, on the internet and the private network
	hostPort    = 18081 // a service on the node host that clients must not reach
	apiPort     = 8585  // the node API port, which clients may reach
	bannerSMTP  = "220 dvpnd-it smtp"
	bannerHTTP  = "dvpnd-it target"
	bannerHost  = "dvpnd-it host"
	bannerAPI   = "dvpnd-it api"
	dialTimeout = 5 * time.Second
)

// env is what run.sh tells the tests about the networks around them.
var env struct {
	enabled bool
	public  string // the target's address on the network that stands for the internet
	name    string // a name of the target, resolved by the host's resolver
	private string // the target's address on the provider's private network
	self    string // this host's own address on the internet network
}

func TestMain(m *testing.M) {
	switch os.Getenv("DVPND_IT_ROLE") {
	case "target":
		serveTarget()
		return
	case "dial":
		dialTargets(os.Args[1:])
		return
	}

	env.enabled = os.Getenv("DVPND_IT") == "1"
	env.public = os.Getenv("DVPND_IT_PUBLIC")
	env.name = os.Getenv("DVPND_IT_NAME")
	env.private = os.Getenv("DVPND_IT_PRIVATE")
	env.self = os.Getenv("DVPND_IT_SELF")

	if env.enabled {
		// The host's own services: one clients must not reach, and the API.
		for _, l := range []struct {
			addr, banner string
		}{
			{fmt.Sprintf(":%d", hostPort), bannerHost},
			{fmt.Sprintf(":%d", apiPort), bannerAPI},
		} {
			if err := serveBanner(l.addr, l.banner); err != nil {
				fmt.Fprintln(os.Stderr, "integration:", err)
				os.Exit(1)
			}
		}
	}

	os.Exit(m.Run())
}

// requireEnv skips a test outside the containers run.sh sets up, and fails it
// inside them when they are not what the test needs.
func requireEnv(t *testing.T) {
	t.Helper()

	if !env.enabled {
		t.Skip("runs only in the containers test/integration/run.sh sets up")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the integration tests run as root, as the node does")
	}
	if env.public == "" || env.private == "" || env.self == "" || env.name == "" {
		t.Fatal("DVPND_IT_PUBLIC, DVPND_IT_NAME, DVPND_IT_PRIVATE and DVPND_IT_SELF must be set")
	}
}

// serveBanner accepts connections on addr and writes one line to each.
func serveBanner(addr, banner string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(conn, banner+"\n")
			_ = conn.Close()
		}
	}()

	return nil
}

// serveTarget is the target role: a service on targetPort and a mail relay
// on port 25, on every address of the target.
func serveTarget() {
	if err := serveBanner(fmt.Sprintf(":%d", targetPort), bannerHTTP); err != nil {
		panic(err)
	}
	if err := serveBanner(fmt.Sprintf(":%d", common.SMTPPort), bannerSMTP); err != nil {
		panic(err)
	}
	fmt.Println("target ready")
	select {}
}

// dialTargets is the dial role: one line per target, "<target> reached" or
// "<target> blocked: <reason>".
func dialTargets(targets []string) {
	d := &net.Dialer{Timeout: dialTimeout}
	for _, target := range targets {
		conn, err := d.Dial("tcp", target)
		if err == nil {
			_, err = readBanner(conn)
		}
		if err != nil {
			fmt.Printf("%s blocked: %v\n", target, err)
			continue
		}
		fmt.Printf("%s reached\n", target)
	}
}

// readBanner reads the line a banner service writes and closes conn.
func readBanner(conn net.Conn) (string, error) {
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(line), nil
}

// dialFunc dials one target and returns what its service wrote.
type dialFunc func(target string) (string, error)

// socks5 dials through a SOCKS5 proxy on proxyAddr, handing it host names
// unresolved so the node, not the test, resolves them.
func socks5(proxyAddr string) dialFunc {
	return func(target string) (string, error) {
		host, portStr, err := net.SplitHostPort(target)
		if err != nil {
			return "", err
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return "", err
		}

		conn, err := net.DialTimeout("tcp", proxyAddr, dialTimeout)
		if err != nil {
			return "", err
		}
		_ = conn.SetDeadline(time.Now().Add(2 * dialTimeout))

		if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
			conn.Close()
			return "", err
		}
		reply := make([]byte, 2)
		if _, err = io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
			conn.Close()
			return "", fmt.Errorf("socks greeting: %v %v", reply, err)
		}

		req := []byte{5, 1, 0}
		if ip := net.ParseIP(host); ip == nil {
			req = append(append(req, 3, byte(len(host))), host...)
		} else if ip4 := ip.To4(); ip4 != nil {
			req = append(append(req, 1), ip4...)
		} else {
			req = append(append(req, 4), ip.To16()...)
		}
		req = binary.BigEndian.AppendUint16(req, uint16(port))
		if _, err = conn.Write(req); err != nil {
			conn.Close()
			return "", err
		}
		head := make([]byte, 4)
		if _, err = io.ReadFull(conn, head); err != nil {
			conn.Close()
			return "", err
		}
		if head[1] != 0 {
			conn.Close()
			return "", fmt.Errorf("socks connect refused (reply %d)", head[1])
		}
		var skip int
		switch head[3] {
		case 1:
			skip = 4 + 2
		case 4:
			skip = 16 + 2
		case 3:
			n := make([]byte, 1)
			if _, err = io.ReadFull(conn, n); err != nil {
				conn.Close()
				return "", err
			}
			skip = int(n[0]) + 2
		}
		if _, err = io.ReadFull(conn, make([]byte, skip)); err != nil {
			conn.Close()
			return "", err
		}

		// A proxy client may confirm the connection before the node has
		// opened it, so only the target's banner shows it was reached.
		return readBanner(conn)
	}
}

// reach is one destination and whether a client may reach it.
type reach struct {
	target string
	want   bool
}

// clientView is what a client of the node must and must not reach under the
// default policy: the internet, by address and by name, and the node API;
// not the private network, the host's own services by any address or name
// (rebind.test is a name the host resolves to loopback), or a mail relay.
func clientView(allowSMTP bool) []reach {
	return []reach{
		{net.JoinHostPort(env.public, strconv.Itoa(targetPort)), true},
		{net.JoinHostPort(env.name, strconv.Itoa(targetPort)), true},
		{net.JoinHostPort(env.self, strconv.Itoa(apiPort)), true},
		{net.JoinHostPort(env.public, strconv.Itoa(common.SMTPPort)), allowSMTP},
		{net.JoinHostPort(env.private, strconv.Itoa(targetPort)), false},
		{net.JoinHostPort(env.self, strconv.Itoa(hostPort)), false},
		{net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), false},
		{net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)), false},
		{net.JoinHostPort("::1", strconv.Itoa(hostPort)), false},
		{net.JoinHostPort("localhost", strconv.Itoa(hostPort)), false},
		{net.JoinHostPort("rebind.test", strconv.Itoa(hostPort)), false},
	}
}

// checkReach dials every destination and fails the test where the outcome is
// not the wanted one.
func checkReach(t *testing.T, dial dialFunc, cases []reach) {
	t.Helper()

	for _, c := range cases {
		banner, err := dial(c.target)
		reached := err == nil && banner != ""
		switch {
		case c.want && !reached:
			t.Errorf("%s must be reachable, but: %v", c.target, err)
		case !c.want && reached:
			t.Errorf("%s must be blocked, but a client reached it (%q)", c.target, banner)
		}
	}
}

// waitReach retries dial until target answers, so a test does not race the
// daemon or the client starting up.
func waitReach(t *testing.T, dial dialFunc, target string, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for {
		_, err := dial(target)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not reachable after %s: %v", target, within, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitBlocked retries dial until target no longer answers.
func waitBlocked(t *testing.T, dial dialFunc, target string, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for {
		if _, err := dial(target); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still reachable %s after the peer was removed", target, within)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// runDial runs the dial role, after prefix (ip netns exec …) and as attr
// says (another account), and answers from what it reached.
func runDial(t *testing.T, prefix []string, attr *syscall.SysProcAttr, cases []reach) dialFunc {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := append(append(append([]string(nil), prefix...), self), targetsOf(cases)...)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = attr
	cmd.Env = append(os.Environ(), "DVPND_IT_ROLE=dial")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dial role: %v\n%s", err, out)
	}

	results := map[string]error{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		target, rest, _ := strings.Cut(line, " ")
		if rest == "reached" {
			results[target] = nil
			continue
		}
		results[target] = errors.New(strings.TrimPrefix(rest, "blocked: "))
	}

	return func(target string) (string, error) {
		err, ok := results[target]
		if !ok {
			return "", errors.New("not dialled")
		}
		if err != nil {
			return "", err
		}

		return "reached", nil
	}
}

func targetsOf(cases []reach) []string {
	list := make([]string, 0, len(cases))
	for _, c := range cases {
		list = append(list, c.target)
	}

	return list
}

// writeTLS gives a node home the self-signed certificate the node creates.
func writeTLS(t *testing.T, home string) {
	t.Helper()

	cert, key, err := common.SelfSignedCertificate("dvpnd", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"tls.crt": cert, "tls.key": key} {
		if err := os.WriteFile(filepath.Join(home, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// noChainsLeft fails the test if any of the node's iptables chains or jumps
// is still installed.
func noChainsLeft(t *testing.T) {
	t.Helper()

	for _, tool := range []string{"iptables", "ip6tables"} {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		out, err := exec.Command(tool, "-S").CombinedOutput()
		if err != nil {
			if tool == "ip6tables" {
				continue // a kernel without IPv6
			}
			t.Fatalf("%s -S: %v\n%s", tool, err, out)
		}
		if strings.Contains(string(out), "DVPND") {
			t.Errorf("%s still holds the node's rules after Stop:\n%s", tool, out)
		}
	}
}

// processes lists the pids of running processes named comm whose command
// line contains arg, with each one's real user id.
func processes(t *testing.T, comm, arg string) map[int]int {
	t.Helper()

	found := map[int]int{}
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, dir := range dirs {
		name, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil || strings.TrimSpace(string(name)) != comm {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		if !strings.Contains(string(cmdline), arg) {
			continue
		}
		status, _ := os.ReadFile(filepath.Join(dir, "status"))
		for _, line := range strings.Split(string(status), "\n") {
			if fields := strings.Fields(line); len(fields) > 1 && fields[0] == "Uid:" {
				pid, _ := strconv.Atoi(filepath.Base(dir))
				uid, _ := strconv.Atoi(fields[1])
				found[pid] = uid
			}
		}
	}

	return found
}

// startClient runs a protocol client the test drives and stops it when the
// test ends; its output is kept for the failure message.
func startClient(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()

	log, err := os.Create(filepath.Join(t.TempDir(), name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			out, _ := os.ReadFile(log.Name())
			t.Logf("%s client output:\n%s", name, out)
		}
	})

	return cmd
}
