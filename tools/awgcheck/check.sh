#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# The two-container AmneziaWG check (docs/protocols.md, AmneziaWG): the node
# image built from this tree, with the 3.1 engine and both tiers up, against
# a client with the engine the apps bundle and a client with the 3.1 engine,
# each on each tier. Needs Docker, /dev/net/tun and the internet (the clients
# fetch over HTTPS through the node).
#
# Expected: the app-era client and the 3.1 client both tunnel on the default
# tier, the 3.1 client tunnels on the 3.1 tier, and the app-era client cannot
# even parse the 3.1 tier's configuration. Exits non-zero otherwise.
#
# Overrides: APP_GO_TAG/APP_TOOLS_TAG (the apps' engine), NEW_GO_TAG/
# NEW_TOOLS_TAG (the node's), BUILD_NETWORK (docker build --network; "host"
# by default because BuildKit's own network had no IPv6 route to the Go
# module proxy on the machine this was written on).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP_GO_TAG="${APP_GO_TAG:-v0.2.19}"
APP_TOOLS_TAG="${APP_TOOLS_TAG:-v1.0.20260618-2}"
NEW_GO_TAG="${NEW_GO_TAG:-v3.1.20260828}"
NEW_TOOLS_TAG="${NEW_TOOLS_TAG:-v3.1.20260812}"
BUILD_NETWORK="${BUILD_NETWORK:-host}"
USERSPACE="-e WG_QUICK_USERSPACE_IMPLEMENTATION=amneziawg-go" # never the host's kernel module

WORK="$(mktemp -d)"
cleanup() {
  set +e
  docker rm -f awgcheck-node >/dev/null 2>&1
  docker network rm awgcheck >/dev/null 2>&1
  # The node ran as root and wrote its home under $WORK/out with mode 0700.
  docker run --rm -v "$WORK/out:/out" alpine:3.24 sh -c 'rm -rf /out/*' >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== building the node image and the two client images"
docker build --network="$BUILD_NETWORK" -t dvpnd:awgcheck "$ROOT" >"$WORK/node-build.log" 2>&1 || { tail -30 "$WORK/node-build.log"; exit 1; }
docker build --network="$BUILD_NETWORK" --build-arg GO_TAG="$APP_GO_TAG" --build-arg TOOLS_TAG="$APP_TOOLS_TAG" \
  -t awgcheck-client:app "$ROOT/tools/awgcheck/client" >"$WORK/client-app-build.log" 2>&1 || { tail -30 "$WORK/client-app-build.log"; exit 1; }
docker build --network="$BUILD_NETWORK" --build-arg GO_TAG="$NEW_GO_TAG" --build-arg TOOLS_TAG="$NEW_TOOLS_TAG" \
  -t awgcheck-client:new "$ROOT/tools/awgcheck/client" >"$WORK/client-new-build.log" 2>&1 || { tail -30 "$WORK/client-new-build.log"; exit 1; }
CGO_ENABLED=0 go build -C "$ROOT" -trimpath -o "$WORK/awgnode" ./tools/awgcheck

mkdir -p "$WORK/out" && chmod 0777 "$WORK/out"
docker network create --subnet 172.30.0.0/24 awgcheck >/dev/null
# shellcheck disable=SC2086
docker run -d --name awgcheck-node --network awgcheck --ip 172.30.0.10 $USERSPACE \
  --device /dev/net/tun --cap-add NET_ADMIN --cap-add NET_RAW \
  --sysctl net.ipv4.ip_forward=1 --sysctl net.ipv6.conf.all.disable_ipv6=0 --sysctl net.ipv6.conf.all.forwarding=1 \
  -v "$WORK/awgnode:/usr/local/bin/awgnode:ro" -v "$WORK/out:/out" \
  --entrypoint /usr/local/bin/awgnode dvpnd:awgcheck -home /out/home -out /out -endpoint 172.30.0.10 >/dev/null
for _ in $(seq 1 40); do docker logs awgcheck-node 2>&1 | grep -q '^ready' && break; sleep 1; done
docker logs awgcheck-node 2>&1 | grep -q '^ready' || { echo "the node did not come up:"; docker logs awgcheck-node; exit 1; }

echo "== node interfaces"
docker exec awgcheck-node sh -c 'for i in awg0 awg1; do echo "$i: port $(awg show $i listen-port), mtu $(cat /sys/class/net/$i/mtu)"; done'

failed=0
run_case() { # name image conf gateway expect(ok|fail)
  echo; echo "== $1"
  # shellcheck disable=SC2086
  out="$(docker run --rm --network awgcheck $USERSPACE --device /dev/net/tun --cap-add NET_ADMIN --cap-add NET_RAW \
    --sysctl net.ipv6.conf.all.disable_ipv6=0 -v "$WORK/out:/out:ro" "$2" "/out/$3" "$4" 2>&1 || true)"
  echo "$out" | grep -E "^(ping|https|download|RESULT|Line unrecognized)" || true
  case "$5" in
    ok) echo "$out" | grep -q "RESULT: handshake ok" && echo "$out" | grep -q "^ping .*: ok" || { echo "UNEXPECTED: this pairing should tunnel"; failed=1; } ;;
    fail) echo "$out" | grep -q "RESULT: awg-quick up FAILED" || { echo "UNEXPECTED: this pairing should not come up"; failed=1; } ;;
  esac
}
# The node derives each tier's tunnel network from its key and writes the
# gateway addresses next to the client configurations.
GW2="$(cat "$WORK/out/gateway2")"
GW3="$(cat "$WORK/out/gateway3")"
echo "== tunnel gateways: default tier $GW2, 3.1 tier $GW3"
run_case "A: the apps' engine ($APP_GO_TAG, tools $APP_TOOLS_TAG) on the default tier" awgcheck-client:app client2.conf "$GW2" ok
run_case "B: the 3.1 engine ($NEW_GO_TAG, tools $NEW_TOOLS_TAG) on the 3.1 tier" awgcheck-client:new client3.conf "$GW3" ok
run_case "C: the 3.1 engine on the default tier" awgcheck-client:new client2.conf "$GW2" ok
run_case "D: the apps' engine on the 3.1 tier (must fail: it does not know the keys)" awgcheck-client:app client3.conf "$GW3" fail

echo; echo "== node-side counters"
docker logs awgcheck-node 2>&1 | grep '^peer' | tail -2
docker stop -t 20 awgcheck-node >/dev/null
docker logs awgcheck-node 2>&1 | grep -q '^stopping: <nil>' || { echo "UNEXPECTED: the node did not stop cleanly"; failed=1; }

[ "$failed" -eq 0 ] && echo "RESULT: all four pairings behaved as expected" || echo "RESULT: FAILED"
exit "$failed"
