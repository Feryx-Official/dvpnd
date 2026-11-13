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
- **[RT-19] The node overwrites no tunnel configuration and takes down no interface that is
  not its own.** The WireGuard and AmneziaWG configuration it writes starts with a marker
  line; a file without it is the node's only when it holds the node's private key (a
  release before the marker wrote it), and is then rewritten with the marker, readable by
  root only. An interface left up is recreated only when `wg show` gives the node's private
  key for it. On a host with its own `wg0`, the administrator's file, key and peers were
  replaced, an existing file kept its mode, and an interface of that name was taken down.
- **[RT-20] A configuration file is replaced whole, never rewritten in place.** The node
  writes the new content to a file beside it and renames that over the old one, so a crash
  or a full disk while writing leaves the old file as it was; rewritten in place, it could
  be left empty or cut short. A path that is a symlink is written where it points, the
  link kept. A file mounted on its own (`docker run -v file:file`), which cannot be
  replaced, is rewritten in place.
- **[RT-21] A key in the node's configuration file that no setting takes is named in the
  log at start.** The node still starts and ignores the key. Viper ignores it without a
  word, so a misspelt key silently fell back to the default; a file carried over from the
  upstream node keeps keys this node no longer has, so refusing to start would turn an
  upgrade into an outage.
- **[RT-22] A value the node writes into a daemon's configuration reads back as itself.**
  Strings in xray's and v2ray's JSON go through `json`, and OpenVPN's paths and account
  names are quoted the way OpenVPN reads a quoted argument, so a space, a quote or a
  backslash in the node's home or in the REALITY server name can neither break the file nor
  add a directive. A value with a line break, which no OpenVPN quoting carries, stops the
  start. Fields the node sets itself, V2Ray's security and TLS paths, take no key from the
  file.
- **[RT-23] Reading a protocol's file never draws a value.** A listen port or key the file
  lacks stops the start, naming it. Filled at random as the file was read, it gave the node
  a new port, or a new WireGuard key, at every start, while clients held the old one. Only
  V2Ray's `[api]` port, a loopback port that files from before the section lack (RT-9), is
  still drawn. XRAY's control port must differ from its VLESS port.
- **[RT-24] The installer opens every port sshd listens on before it turns ufw on.** The
  ports come from `sshd -T`, which reads the drop-in files and ListenAddress as sshd does.
  The installer read the main file's first `Port` line only: a port set in
  `/etc/ssh/sshd_config.d`, as cloud images do, was missed, ufw opened 22, and enabling it
  shut the operator out.
- **[RT-25] The installer writes the node's configuration and protocol file whole or not at
  all.** It writes them in a scratch home and moves each into place once every setting is
  in. A setting that failed after `config init` left a partial file, and the next run kept
  it as the node's configuration.
- **[RT-26] The installer downloads into a directory of its own and checks each download
  against a checksum the script pins.** It runs as root: a fixed path under `/tmp` can be
  created first by any local user, and the Go checksum, fetched from the site that served
  the tarball, proved the download intact but not authentic.
- **[RT-27] The Docker runner's setup keeps what the host already has.** It installs no
  Docker of its own: it ran get.docker.com's script as root, unchecked. It writes
  `/etc/docker/daemon.json` and restarts Docker only on a host with no such file: it
  rewrote the file, dropping the host's settings, and the restart restarted every
  container. It keeps an existing TLS certificate, which clients pin, and installs no
  package that replaces the firewall: the iptables-persistent it installed removes ufw.
- **[RT-28] The Docker runner starts each node type as the operator guide's `docker run`
  does, acts on its own container only, and runs only an image it checked when it can.**
  Its capabilities, devices, sysctls, modules volume, restart policy and log caps are the
  guide's; they had drifted to no restart policy, no log caps and a writable
  `/lib/modules`. It reads each setting from its own table, matched whole: a moniker
  holding "type" was read as the node type, and nothing started. It finds its container
  by its whole name, where `docker ps --filter name=` matched any name holding it. With
  cosign installed it checks a pulled image's signature as the guide does and removes one
  that fails, and it never pulls on the way to a start.
- **[RT-29] Under the systemd unit, what the node and its daemons create is root's only
  unless they set its mode, and the unit's tasks are capped.** `UMask=0077` and
  `TasksMax=4096`: with systemd's default umask, 0022, a file a daemon wrote without
  setting its mode was readable by every account on the host, and nothing bounded the tasks
  a daemon could start. Read from inside the unit in the systemd test stage.
- **[RT-30] The node image and the installer run the same pinned daemons, from base images
  pinned by digest.** xray, Hysteria and AmneziaWG are pinned to the same release in the
  `Dockerfile` and `scripts/install.sh`, each checked against a sha256 or a full commit
  hash; every Dockerfile in the tree builds on base images pinned by digest, in a directory
  Dependabot watches, and on one Alpine release across its stages. The versions were
  written twice by hand, hnsd's commit was checked by a 12-character prefix, hnsd was built
  on one Alpine release and run on another, and two Dockerfiles took whatever their tag
  pointed at.
- **[RT-31] CI, the installer and the node image build with one Go toolchain.** `go.mod`
  names it on its `toolchain` line, the image's build stage is that Go release, and the
  installer installs it when the host has no Go recent enough. `go.mod` named only the
  minimum language version: CI tested on 1.26.10 and installer builds used it, with a
  reachable `net/http` advisory, while the image was compiled with 1.27.4.
- **[RT-32] Every pull request and push to `main` runs the security checks, and what they
  find fails them.** govulncheck fails on a vulnerability the code reaches that
  `SECURITY.md` does not explain under "Known dependency advisories"; golangci-lint, gosec
  included, on any finding not excused on its own line with the reason; CodeQL runs on
  both, not only weekly. `main`'s ruleset, `.github/rulesets/main-checks.json`, requires
  every job CI and CodeQL run on a pull request, and holds merges to CodeQL's alerts.
  govulncheck only warned, golangci-lint ran with its exit code forced to 0, and CodeQL saw
  code up to a week after it merged: a pull request that added a reachable vulnerability
  or a gosec finding was green.
- **[RT-33] A release publishes only a tag the installer would build, from a commit CI
  passed.** The release workflow's first job checks the tag with the installer's own
  `verify_tag` and `TAG_SIGNER`, read from `scripts/install.sh`, then waits for a CI run of
  the tagged commit to pass; every other job needs it, and CI runs on each release tag
  pushed. A published release went straight to build, push and signing: neither the tag's
  signature nor CI, the integration suite among it, was checked.
- **[RT-34] Each release carries its installer, signed by the release workflow.** The
  release workflow, once the gate passed, signs `scripts/install.sh` keylessly, as it signs
  the image, and attaches it with its Sigstore bundle; `docs/operator.md` checks it with
  cosign against that workflow's identity. The installer, which runs as root, came only
  from the `main` branch or from a clone the operator checked by hand.
