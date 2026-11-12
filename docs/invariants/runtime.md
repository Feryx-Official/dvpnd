# Runtime: how the node runs the daemons, and what it leaves behind

The protocol daemons parse hostile traffic, so they're not trusted with the host, and
the node must not leave a tunnel, a rule or a process behind when it stops. IDs as in
`test/README.md`.

- **[RT-1] The protocol daemons run as the unprivileged `dvpnd-proxy` account.** When
  the node runs as root and the account exists, the daemons start as it, with their
  files in the runtime directory and no access to the node's home or keys; OpenVPN drops
  to it once its tunnel is up. The Handshake resolver, which parses DNS from every client,
  runs as the account too, keeping only the right to bind port 53, and stops with the
  node; it ran as root and outlived the node. It stays outside the proxies' egress chain,
  which was never tried with it.
- **[RT-2] However the node stops after its service has started, the service is stopped
  first.** A stop signal, a job that panics, or a step that fails after `Start` all take
  the tunnel down, remove the firewall rules and stop the daemon, so none of it outlives
  the node. The stop signals are caught from before `Start`: a SIGTERM while the node was
  still starting ended it at once, with the tunnel and the firewall rules left up.
- **[RT-3] A daemon is stopped with SIGTERM and killed if it doesn't exit in time; a
  daemon that exits on its own is reaped.** A node killed outright runs no `Stop`, so the
  kernel sends its daemons SIGTERM when it dies; outside systemd or Docker they outlived
  it. Every daemon is forked from one thread that lives as long as the node, since the
  signal follows the forking thread, not the process.
- **[RT-4] IP forwarding is switched on from the node, read before it is written.**
  `/proc/sys` is read-only in an unprivileged container, where the runner sets the sysctl
  instead; reading first lets that node start.
- **[RT-5] A call to a proxy's control API is bounded in time, and waits for a proxy
  that is still starting.** A proxy that stops answering must not hold a handshake or a
  job forever; a handshake right after the node starts must not fail because the
  proxy's API isn't up yet.
- **[RT-6] A service refuses to initialise without its daemon's binary.** The operator
  learns at `init`, not from a node that starts and serves nothing.
- **[RT-7] Once the service stops, no daemon and no firewall rule of it is left.**
  Checked end to end with the real daemons, under the systemd unit as shipped too.
- **[RT-8] WireGuard accepts the replies coming back into the tunnel whatever the host's
  FORWARD policy is.** On a host where it's DROP (ufw, or Docker installed) every reply
  to a peer was dropped and TCP through the tunnel hung.
- **[RT-9] A configuration file written by an earlier release still starts the node.**
  Files without `[egress]`, without V2Ray's `[api]`, without AmneziaWG's `[v3]` or with
  OpenVPN's old management port read as before; an upgrade must not need a hand edit.
- **[RT-10] A command that fails exits with a non-zero status.** The installer runs
  under `set -e`, and systemd and Docker read the status: a failure reported as success
  goes unnoticed, and a node that could not start was logged as "Deactivated successfully".
- **[RT-11] The installer waits for the package manager rather than failing when it is busy.**
  A fresh server runs unattended-upgrades in its first minutes, when an operator runs the
  installer; its first `apt-get` found the lock taken and the install stopped (exit 100).
- **[RT-12] Running the installer again adds nothing to root's git configuration.** The
  build needs a `safe.directory` entry for the source tree, and an upgrade is a re-run:
  each run added another copy. A run leaves one entry for the tree, folds the copies
  earlier runs left into it, and keeps the operator's own entries.
- **[RT-13] `config set` refuses a key its file does not have.** Viper accepts any key,
  and the file was saved without it and the command exited 0: a misspelt key was lost
  without a word, and the Docker runner stored the operator's prices under keys the node
  no longer had, so the node it set up had no prices and refused to start.
- **[RT-14] The installer builds only the release tag asked for, signed by the maintainer
  and fetched from the repository's current home.** The signature covers the tag object,
  not the ref that points at it: an older release's signed tag served under a newer name
  passed the check and was built as root, so the object must name the tag asked for. An
  upgrade fetched from the origin the first install cloned, which may be a former home
  (LIC-8); origin is pointed at the current home before every fetch.
- **[RT-15] A node whose protocol daemon exits stops, with an error, so its service manager
  starts it again.** The daemon (xray, v2ray, Hysteria, OpenVPN), OpenVPN's management
  channel and Hysteria's authentication hook are watched from `Start` on. A node whose
  daemon had died stayed up and listed as active on the chain, and Hysteria and OpenVPN
  nodes kept admitting clients who had paid for a session they could not serve. A daemon
  that exits within a second of starting stops the node before it goes active.
- **[RT-16] A node goes active on the chain only once it can serve: its API port is bound
  and its certificate loaded first.** A port in use or a missing certificate showed only
  after the node had gone active, and every restart went active again.
- **[RT-17] An interface or uplink name is checked before it reaches a command or a daemon's
  configuration.** Up to 15 letters, digits, `_`, `.` or `-`, starting with a letter or
  digit, for WireGuard, AmneziaWG and OpenVPN, and for the name the default route gives.
  wg-quick runs PostUp through `eval` as root, and WireGuard and AmneziaWG wrote the uplink
  into it unchecked: a `;` in the setting, or in the detected name, split it into a second
  command.
- **[RT-18] Every tool the node runs on the host finishes within a bound, and iptables
  waits for the xtables lock.** iptables, ip, wg and awg get 15 seconds, wg-quick and
  awg-quick two minutes; a tool still running then gets SIGTERM, and SIGKILL after five
  seconds more. AddPeer runs wg under the admission lock, so a wg that hung held every
  handshake. iptables exits at once when another program (Docker, ufw, firewalld) holds
  the lock unless told to wait, and the node drops a failed removal without a word; every
  call, the ones in wg-quick's PostUp too, waits up to five seconds.
