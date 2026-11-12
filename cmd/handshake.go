// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"net"
	"strconv"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"

	"github.com/Feryx-Official/dvpnd/v9/services/common"
)

// hnsdBinary is the Handshake resolver's command; tests point it elsewhere.
var hnsdBinary = "hnsd"

// handshakeRestartDelay is the pause before the Handshake resolver is started
// again after it exits, so a resolver that cannot run does not spin.
var handshakeRestartDelay = 15 * time.Second

// resolverStopTimeout is how long the resolver has to exit on SIGTERM.
const resolverStopTimeout = 5 * time.Second

// hnsdArgs is the Handshake resolver's command line. It listens on the node's
// tunnel address only: on every address, as upstream had it, a node whose
// port 53 was reachable would be an open resolver anyone could use against
// third parties. It keeps no log of the names clients look up.
func hnsdArgs(peers uint64, host net.IP) []string {
	return []string{
		"--log-file", "/dev/null",
		"--pool-size", strconv.FormatUint(peers, 10),
		"--rs-host", net.JoinHostPort(host.String(), "53"),
	}
}

// resolver keeps the Handshake resolver running until Stop. Since it parses
// DNS from every client, it runs as the proxy account when the node has one,
// keeping only the right to bind port 53. It stays outside the proxies'
// egress chain, which was never tried with hnsd: its two halves talk over
// loopback, which the chain refuses but for the ports it lists.
type resolver struct {
	stop chan struct{}
	done chan struct{}
}

func startHandshake(log cmtlog.Logger, peers uint64, host net.IP) *resolver {
	r := &resolver{stop: make(chan struct{}), done: make(chan struct{})}
	go r.run(log, peers, host)

	return r
}

func (r *resolver) run(log cmtlog.Logger, peers uint64, host net.IP) {
	defer close(r.done)
	for {
		log.Info("Starting the Handshake resolver", "address", host)
		p, err := common.StartUnprivileged(hnsdBinary, hnsdArgs(peers, host), nil, 53)
		if err != nil {
			log.Error("failed to start the Handshake resolver", "error", err)
		} else {
			select {
			case <-p.Done():
				log.Error("handshake process exited unexpectedly", "error", p.Err())
			case <-r.stop:
				if err := p.Stop(resolverStopTimeout); err != nil {
					log.Error("failed to stop the Handshake resolver", "error", err)
				}

				return
			}
		}

		select {
		case <-time.After(handshakeRestartDelay):
		case <-r.stop:
			return
		}
	}
}

// Stop ends the resolver and waits until it has.
func (r *resolver) Stop() {
	close(r.stop)
	<-r.done
}
