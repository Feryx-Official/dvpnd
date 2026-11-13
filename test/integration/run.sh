#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# The integration suite: every protocol with its real daemon and a real
# client, in containers that stand in for the node host, the internet and the
# provider's private network (test/README.md). Needs Docker and
# the internet (the image build, and XRAY REALITY's handshake target).
#
#   test/integration/run.sh [stage...]     stages: proxies tunnels systemd (all by default)
#
# proxies  V2Ray, XRAY (REALITY and TLS) and Hysteria2 in a container with
#          exactly the capabilities scripts/runner.sh grants a proxy node,
#          plus the proxy account's kernel chain and the tunnel chains in the
#          real iptables.
# tunnels  WireGuard, AmneziaWG (both tiers) and OpenVPN with a client in a
#          network namespace (a privileged container). The WireGuard server
#          uses the host kernel's in-tree wireguard module, which the kernel
#          loads on demand, as for any WireGuard node in Docker; AmneziaWG
#          runs in userspace.
# systemd  XRAY, Hysteria2 and the OpenVPN server under scripts/dvpnd.service
#          itself (a drop-in replaces only ExecStart), in a Debian container
#          running systemd, and the unit's own limits read from inside it.
#          V2Ray is left out: the image's build is Alpine's.
#
# Exits non-zero if any stage fails. BUILD_NETWORK sets docker build
# --network ("host" by default, as in tools/awgcheck/check.sh).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BUILD_NETWORK="${BUILD_NETWORK:-host}"
STAGES=("$@")
[[ ${#STAGES[@]} -gt 0 ]] || STAGES=(proxies tunnels systemd)

IMAGE=dvpnd:it
SYSTEMD_IMAGE=dvpnd-it-systemd:latest
PUBLIC_NET=dvpnd-it-public   # stands for the internet
PRIVATE_NET=dvpnd-it-private # stands for the provider's private network
TARGET_PUBLIC=203.0.113.10
TARGET_PRIVATE=10.99.0.10
TARGET_NAME=target.dvpnd-it.test

CONTAINERS=(dvpnd-it-target dvpnd-it-proxies dvpnd-it-tunnels dvpnd-it-systemd)
remove_containers() {
  for c in "${CONTAINERS[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  docker network rm "$PUBLIC_NET" "$PRIVATE_NET" >/dev/null 2>&1 || true
}
remove_containers # left over from an interrupted run
WORK="$(mktemp -d)"
trap 'remove_containers; rm -rf "$WORK"' EXIT

echo "== building the node image and the test binary"
docker build --network="$BUILD_NETWORK" -t "$IMAGE" "$ROOT" >"$WORK/build.log" 2>&1 ||
  { tail -40 "$WORK/build.log"; exit 1; }
CGO_ENABLED=0 go test -C "$ROOT" -c -tags integration -trimpath -o "$WORK/dvpnd-it" ./test/integration
chmod 0755 "$WORK" "$WORK/dvpnd-it"

docker network create --subnet 203.0.113.0/24 "$PUBLIC_NET" >/dev/null
docker network create --internal --subnet 10.99.0.0/24 "$PRIVATE_NET" >/dev/null
docker run -d --name dvpnd-it-target --network "$PUBLIC_NET" --ip "$TARGET_PUBLIC" \
  --network-alias "$TARGET_NAME" -e DVPND_IT_ROLE=target \
  -v "$WORK/dvpnd-it:/usr/local/bin/dvpnd-it:ro" --entrypoint /usr/local/bin/dvpnd-it "$IMAGE" >/dev/null
docker network connect --ip "$TARGET_PRIVATE" "$PRIVATE_NET" dvpnd-it-target

# node_env <own public address>: what the tests are told about the networks.
node_env() {
  echo "-e DVPND_IT=1 -e DVPND_IT_PUBLIC=$TARGET_PUBLIC -e DVPND_IT_PRIVATE=$TARGET_PRIVATE" \
    "-e DVPND_IT_NAME=$TARGET_NAME -e DVPND_IT_SELF=$1"
}

# run_node <name> <public ip> <private ip> <docker flags...> -- <test flags...>
run_node() {
  local name=$1 pub=$2 priv=$3; shift 3
  local flags=() args=()
  while [[ $# -gt 0 && $1 != -- ]]; do flags+=("$1"); shift; done
  shift
  args=("$@")
  # shellcheck disable=SC2046
  docker create --name "$name" --network "$PUBLIC_NET" --ip "$pub" --add-host rebind.test:127.0.0.1 \
    $(node_env "$pub") -v "$WORK/dvpnd-it:/usr/local/bin/dvpnd-it:ro" "${flags[@]}" \
    --entrypoint /usr/local/bin/dvpnd-it "$IMAGE" -test.v -test.count=1 "${args[@]}" >/dev/null
  docker network connect --ip "$priv" "$PRIVATE_NET" "$name"
  docker start -a "$name"
}

stage_proxies() {
  # The capabilities scripts/runner.sh gives a proxy node, read from it so
  # the two cannot drift apart.
  local caps
  read -r -a caps <<<"$(sed -n '/^    v2ray | xray | hysteria2)$/,/^      ;;$/p' "$ROOT/scripts/runner.sh" |
    grep -oE -- '--cap-(drop|add) [A-Z_]+' | tr '\n' ' ')"
  [[ ${#caps[@]} -gt 2 ]] || { echo "could not read the proxy capabilities from scripts/runner.sh"; return 1; }
  echo "   with ${caps[*]}"
  run_node dvpnd-it-proxies 203.0.113.2 10.99.0.2 "${caps[@]}" -- \
    -test.run 'TestProxies|TestProxyAccountChain|TestTunnelEgressRules|TestOpenVPNDaemon'
}

stage_tunnels() {
  run_node dvpnd-it-tunnels 203.0.113.3 10.99.0.3 --privileged \
    -e DVPND_IT_TUNNELS=1 -e WG_QUICK_USERSPACE_IMPLEMENTATION=amneziawg-go \
    --sysctl net.ipv4.ip_forward=1 --sysctl net.ipv6.conf.all.disable_ipv6=0 \
    --sysctl net.ipv6.conf.all.forwarding=1 --sysctl net.ipv6.conf.default.forwarding=1 -- \
    -test.run 'TestTunnels|TestOpenVPNDaemon' -test.timeout 20m
}

stage_systemd() {
  mkdir -p "$WORK/ctx"
  docker build --network="$BUILD_NETWORK" --build-arg NODE_IMAGE="$IMAGE" -t "$SYSTEMD_IMAGE" \
    -f "$ROOT/test/integration/systemd.Dockerfile" "$WORK/ctx" >"$WORK/systemd-build.log" 2>&1 ||
    { tail -30 "$WORK/systemd-build.log"; return 1; }

  # The unit as shipped; the drop-in swaps the node for the tests and makes
  # the start wait for them.
  mkdir -p "$WORK/dropin"
  cat >"$WORK/dropin/it.conf" <<EOF
[Service]
Type=oneshot
Restart=no
TimeoutStartSec=900
ExecStart=
ExecStart=/usr/local/bin/dvpnd-it -test.v -test.count=1 -test.run 'TestProxies|TestProxyAccountChain|TestOpenVPNDaemon|TestUnitSandbox'
Environment=DVPND_IT=1 DVPND_IT_PUBLIC=$TARGET_PUBLIC DVPND_IT_PRIVATE=$TARGET_PRIVATE DVPND_IT_NAME=$TARGET_NAME DVPND_IT_SELF=203.0.113.4 DVPND_IT_UNIT=1
EOF
  docker create --name dvpnd-it-systemd --privileged --cgroupns=host \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
    --network "$PUBLIC_NET" --ip 203.0.113.4 --add-host rebind.test:127.0.0.1 \
    -v "$WORK/dvpnd-it:/usr/local/bin/dvpnd-it:ro" \
    -v "$ROOT/scripts/dvpnd.service:/etc/systemd/system/dvpnd.service:ro" \
    -v "$WORK/dropin:/etc/systemd/system/dvpnd.service.d:ro" "$SYSTEMD_IMAGE" >/dev/null
  docker network connect --ip 10.99.0.4 "$PRIVATE_NET" dvpnd-it-systemd
  docker start dvpnd-it-systemd >/dev/null

  local state=""
  for _ in $(seq 1 60); do
    state=$(docker exec dvpnd-it-systemd systemctl is-system-running 2>/dev/null || true)
    [[ $state == running || $state == degraded ]] && break
    sleep 1
  done
  echo "   systemd: $state; unit: $(docker exec dvpnd-it-systemd systemd-analyze verify dvpnd.service 2>&1 | grep -v '^$' || echo verified)"
  local rc=0
  docker exec dvpnd-it-systemd systemctl start dvpnd || rc=$?
  docker exec dvpnd-it-systemd journalctl -u dvpnd -o cat --no-pager | grep -vE '^(Starting|Finished|Started|dvpnd.service: Deactivated|dvpnd.service: Consumed)' || true
  [[ $(docker exec dvpnd-it-systemd systemctl show -p Result --value dvpnd) == success ]] || rc=1
  return $rc
}

failed=()
for stage in "${STAGES[@]}"; do
  echo
  echo "== stage: $stage"
  case $stage in
    proxies | tunnels | systemd) "stage_$stage" || failed+=("$stage") ;;
    *) echo "unknown stage $stage"; failed+=("$stage") ;;
  esac
done

echo
if [[ ${#failed[@]} -gt 0 ]]; then
  echo "RESULT: FAILED: ${failed[*]}"
  exit 1
fi
echo "RESULT: all stages passed (${STAGES[*]})"
