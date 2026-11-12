#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# dvpnd installer for a Debian or Ubuntu host: a VPS, or a machine at home
# behind your own router. It does what docs/operator.md describes by hand:
#
#   1. installs the build tools, Go and the packages the chosen protocol needs
#   2. builds dvpnd from source at a release tag and installs the binary
#   3. writes the configuration and the protocol file
#   4. creates (or recovers) the operator key and a self-signed TLS certificate
#   5. opens the firewall, creates the dvpnd-proxy account and installs the
#      systemd unit
#
# Run it as root:
#
#   curl -fsSL https://raw.githubusercontent.com/Feryx-Official/dvpnd/main/scripts/install.sh \
#     -o install.sh && sudo bash install.sh --moniker "My node"
#
# or from a release tag you checked yourself (docs/operator.md, section 0).
#
# Every step is skipped when its result already exists, so the script can be
# re-run. It never overwrites an existing configuration, key or certificate
# unless told to with --force.

set -Eeuo pipefail

REPO_URL="https://github.com/Feryx-Official/dvpnd.git"
REPO_API="https://api.github.com/repos/Feryx-Official/dvpnd/releases/latest"

# Release tags are signed with the maintainer's SSH key; the installer builds
# a tag only when its signature checks out against this key. It is the same
# key GitHub shows as verified on the tags.
TAG_SIGNER="114076168+proofoftrinity@users.noreply.github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFqdfC3zNhLfD0Hl5H2bvlCvtBtOBhz0VSiietVWvMch"

# Pinned protocol binaries: the versions the client apps are tested against and
# the ones the Docker image builds (see Dockerfile).
XRAY_VERSION="v26.3.27"
XRAY_SHA256="23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae"
HYSTERIA_VERSION="app/v2.10.0"
HYSTERIA_SHA256="04f7804159ef1d798de12a817d73aab4b9040ebe45fc62e223000c5c59e987fe"
AWG_GO_TAG="v3.1.20260828"
AWG_GO_COMMIT="b5928efb6ca19f0153958460c3d141f04abc5c2e"
AWG_TOOLS_TAG="v3.1.20260812"
AWG_TOOLS_COMMIT="ee0f0a9aa34ff0a0da4b3433b9512781cfe02843"

# Go, installed when the host has none recent enough: the toolchain go.mod
# names and the Docker image builds with. The checksums are the ones go.dev
# publishes, pinned here: fetched next to the tarball, they proved it intact,
# not that it was Go's.
GO_VERSION="1.27.4"
GO_SHA256_AMD64="7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840"
GO_SHA256_ARM64="3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92"

# Defaults, all overridable with flags.
NODE_TYPE="wireguard"
TYPE_GIVEN=0
MONIKER=""
GIGABYTE_PRICES="40000000udvpn"
HOURLY_PRICES="97500000udvpn"
API_PORT="8585"
LISTEN_PORT=""
V3_LISTEN_PORT=""
OPENVPN_PROTO="udp"
VERSION=""
SOURCE_DIR=""
NODE_HOME="/root/.dvpnd"
KEY_NAME="operator"
GRANTER=""
RECOVER=0
FORCE=0
YES=0
FIREWALL=1
START=1
BUILD_DIR="/usr/local/src/dvpnd"
DVPND="/usr/local/bin/dvpnd"
SSHD_CONFIG="/etc/ssh/sshd_config"

usage() {
  cat <<EOF
Usage: sudo bash install.sh [options]

Options:
  --type TYPE           wireguard, amneziawg, openvpn, v2ray, xray, hysteria2
                        (default: the existing node's type, else wireguard)
  --moniker NAME        public name of the node, 4-32 characters (asked if missing)
  --gigabyte-price P    price per GB, e.g. ${GIGABYTE_PRICES} (default)
  --hourly-price P      price per hour, e.g. ${HOURLY_PRICES} (default)
  --api-port PORT       TCP port of the node API (default ${API_PORT})
  --listen-port PORT    port of the VPN protocol itself (default: random, printed)
  --v3-listen-port PORT AmneziaWG only: port of the opt-in 3.1 tier (default: random)
  --openvpn-proto P     OpenVPN only: udp (default) or tcp
  --version TAG         release tag to build, e.g. v9.2.0 (default: latest release)
  --source DIR          build from this source tree instead of cloning
  --home DIR            node home directory (default ${NODE_HOME})
  --granter ADDR        sign with a hot key for the node account ADDR (sent1...): the
                        operator key and the earnings stay off this server; prints the
                        authz and feegrant commands to run from that account's wallet
  --recover             import an existing mnemonic instead of creating a key
  --force               overwrite an existing configuration and protocol file
  --no-firewall         do not touch ufw
  --no-start            install the systemd unit but do not start the node
  --yes                 never prompt; fail where a prompt would be needed
  -h, --help            this text
EOF
}

log() { printf '\n==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --type) NODE_TYPE="$2"; TYPE_GIVEN=1; shift 2 ;;
    --moniker) MONIKER="$2"; shift 2 ;;
    --gigabyte-price) GIGABYTE_PRICES="$2"; shift 2 ;;
    --hourly-price) HOURLY_PRICES="$2"; shift 2 ;;
    --api-port) API_PORT="$2"; shift 2 ;;
    --listen-port) LISTEN_PORT="$2"; shift 2 ;;
    --v3-listen-port) V3_LISTEN_PORT="$2"; shift 2 ;;
    --openvpn-proto) OPENVPN_PROTO="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --source) SOURCE_DIR="$2"; shift 2 ;;
    --home) NODE_HOME="$2"; shift 2 ;;
    --granter) GRANTER="$2"; shift 2 ;;
    --recover) RECOVER=1; shift ;;
    --force) FORCE=1; shift ;;
    --no-firewall) FIREWALL=0; shift ;;
    --no-start) START=0; shift ;;
    --yes) YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

# On an existing node the type comes from its configuration, so a re-run (an
# upgrade, or a move with the home directory copied over) installs the packages
# that node needs and keeps its protocol file, without --type being repeated.
# Switching to another type rewrites config.toml, so it takes --force.
if [[ -f "${NODE_HOME}/config.toml" ]]; then
  existing_type=$(awk -F '[="]' '{ gsub(/ /, "") } /^\[node\]/ { f = 1 } f && /^type/ { print $3; exit }' "${NODE_HOME}/config.toml")
  if [[ -n "${existing_type}" ]]; then
    if [[ "${TYPE_GIVEN}" -eq 0 ]]; then
      NODE_TYPE="${existing_type}"
    elif [[ "${NODE_TYPE}" != "${existing_type}" && "${FORCE}" -eq 0 ]]; then
      die "this is a ${existing_type} node; to switch it to ${NODE_TYPE}, add --force (it rewrites config.toml: pass --moniker and your prices again)"
    fi
  fi
fi

case "${NODE_TYPE}" in
  wireguard|amneziawg|openvpn|v2ray|xray|hysteria2) ;;
  *) die "--type must be wireguard, amneziawg, openvpn, v2ray, xray or hysteria2" ;;
esac
case "${OPENVPN_PROTO}" in udp|tcp) ;; *) die "--openvpn-proto must be udp or tcp" ;; esac
# With a granter the key on this server is a hot key of its own, never the
# node account's.
if [[ -n "${GRANTER}" ]]; then
  [[ "${GRANTER}" =~ ^sent1[0-9a-z]{38}$ ]] || die "--granter must be the node account's address (sent1...)"
  [[ "${RECOVER}" -eq 0 ]] || die "--granter creates a fresh hot key; --recover does not apply"
  KEY_NAME="hot"
fi

# ---------------------------------------------------------------- preflight

[[ "${EUID}" -eq 0 ]] || die "run as root: sudo bash install.sh ..."
command -v apt-get >/dev/null || die "this installer supports Debian and Ubuntu (apt-get not found)"

case "$(uname -m)" in
  x86_64) ARCH="amd64"; GO_SHA256="${GO_SHA256_AMD64}" ;;
  aarch64) ARCH="arm64"; GO_SHA256="${GO_SHA256_ARM64}" ;;
  *) die "unsupported CPU architecture: $(uname -m) (x86_64 or aarch64 only)" ;;
esac

# Downloads and builds go to a directory of this run's own: a fixed path under
# /tmp could be put there first by any local user, and this runs as root. The
# scratch home the configuration is written in goes too.
WORK=$(mktemp -d)
trap 'rm -rf "${WORK}" "${NODE_HOME}/.install"' EXIT

ask() {
  # ask VAR "prompt" "default"
  local var="$1" prompt="$2" default="${3:-}" input
  if [[ "${YES}" -eq 1 ]]; then
    [[ -n "${default}" ]] || die "--yes given but ${var} has no value (use the matching flag)"
    printf -v "${var}" '%s' "${default}"
    return
  fi
  if [[ -n "${default}" ]]; then
    read -r -p "${prompt} [${default}]: " input
  else
    read -r -p "${prompt}: " input
  fi
  printf -v "${var}" '%s' "${input:-${default}}"
}

random_port() { shuf -i 10000-60000 -n 1; }

# apt_get runs apt-get, waiting while another apt run holds the package
# manager, as unattended-upgrades does in a fresh server's first minutes. apt
# waits for the dpkg lock itself when told to, but not for the package lists'
# lock, so update is retried, for up to ten minutes.
apt_get() {
  local tries=60 rc
  while :; do
    apt-get -o DPkg::Lock::Timeout=600 "$@" && return 0
    rc=$?
    [[ "$1" == update ]] && ((--tries > 0)) || return "${rc}"
    echo "apt-get update failed (another apt run may hold the package lists); retrying in 10 s"
    sleep 10
  done
}

log "Installing packages"
export DEBIAN_FRONTEND=noninteractive
apt_get update -qq
apt_get install -y -qq ca-certificates curl git build-essential jq openssl openssh-client iproute2 iputils-ping iptables >/dev/null
case "${NODE_TYPE}" in
  wireguard) apt_get install -y -qq wireguard-tools >/dev/null ;;
  openvpn) apt_get install -y -qq openvpn >/dev/null ;;
  amneziawg) apt_get install -y -qq bash >/dev/null ;;
esac

# ---------------------------------------------------------------- source

# verify_tag checks the tag's SSH signature against TAG_SIGNER with ssh-keygen
# directly, since git learned SSH signatures only in 2.34. The signature covers
# the tag object, not the ref that points at it, so the object must also name
# the tag asked for: an older release's signed tag under a newer name is refused.
verify_tag() {
  local dir="$1" tag="$2" tmp rc
  tmp=$(mktemp -d)
  printf '%s\n' "${TAG_SIGNER}" >"${tmp}/allowed_signers"
  if ! git -C "${dir}" cat-file tag "refs/tags/${tag}" >"${tmp}/tag" 2>/dev/null; then
    rm -rf "${tmp}"
    return 1
  fi
  sed -n '/^-----BEGIN SSH SIGNATURE-----$/,$p' "${tmp}/tag" >"${tmp}/sig"
  sed '/^-----BEGIN SSH SIGNATURE-----$/,$d' "${tmp}/tag" >"${tmp}/payload"
  if ! sed '/^$/q' "${tmp}/payload" | grep -qxF "tag ${tag}"; then
    rm -rf "${tmp}"
    return 1
  fi
  ssh-keygen -Y verify -f "${tmp}/allowed_signers" -I "${TAG_SIGNER%% *}" -n git \
    -s "${tmp}/sig" <"${tmp}/payload" >/dev/null 2>&1
  rc=$?
  rm -rf "${tmp}"
  return "${rc}"
}

# fetch_source clones REPO_URL into SRC, or brings an earlier clone up to date.
# An earlier clone keeps the origin it was cloned from, which may be a former
# home of the repository, so origin is pointed at REPO_URL before every fetch.
fetch_source() {
  if [[ -d "${SRC}/.git" ]]; then
    git -C "${SRC}" remote set-url origin "${REPO_URL}" 2>/dev/null ||
      git -C "${SRC}" remote add origin "${REPO_URL}"
    git -C "${SRC}" fetch --quiet --tags origin
  else
    rm -rf "${SRC}"
    git clone --quiet "${REPO_URL}" "${SRC}"
  fi
}

if [[ -n "${SOURCE_DIR}" ]]; then
  [[ -f "${SOURCE_DIR}/go.mod" && -f "${SOURCE_DIR}/main.go" ]] || die "--source ${SOURCE_DIR} is not a dvpnd source tree"
  SRC="${SOURCE_DIR}"
  log "Building from the source tree at ${SRC}"
else
  if [[ -z "${VERSION}" ]]; then
    VERSION=$(curl -fsSL "${REPO_API}" | jq -r '.tag_name // empty') || true
    [[ -n "${VERSION}" ]] || die "could not find the latest release; pass --version vX.Y.Z"
  fi
  SRC="${BUILD_DIR}"
  log "Fetching dvpnd ${VERSION}"
  fetch_source
  verify_tag "${SRC}" "${VERSION}" ||
    die "${VERSION} is not a release tag signed by the maintainer; refusing to build it (build a tree you checked yourself with --source)"
  git -C "${SRC}" checkout --quiet "refs/tags/${VERSION}"
fi

# ---------------------------------------------------------------- Go

go_required=$(awk '$1 == "go" { print $2; exit }' "${SRC}/go.mod")
[[ -n "${go_required}" ]] || die "no go directive in ${SRC}/go.mod"
go_ok=0
if command -v go >/dev/null; then
  have=$(go version | sed -E 's/.*go([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/')
  if [[ "$(printf '%s\n%s\n' "${go_required}" "${have}" | sort -V | head -1)" == "${go_required}" ]]; then
    go_ok=1
  fi
fi
if [[ "${go_ok}" -eq 0 && -x /usr/local/go/bin/go ]]; then
  have=$(/usr/local/go/bin/go version | sed -E 's/.*go([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/')
  if [[ "$(printf '%s\n%s\n' "${go_required}" "${have}" | sort -V | head -1)" == "${go_required}" ]]; then
    go_ok=1
  fi
fi
if [[ "${go_ok}" -eq 0 ]]; then
  log "Installing Go ${GO_VERSION} (the distribution's package is too old or missing)"
  tarball="go${GO_VERSION}.linux-${ARCH}.tar.gz"
  curl -fsSL -o "${WORK}/${tarball}" "https://go.dev/dl/${tarball}"
  echo "${GO_SHA256}  ${WORK}/${tarball}" | sha256sum -c - >/dev/null
  rm -rf /usr/local/go && tar -C /usr/local -xzf "${WORK}/${tarball}"
fi
# A release whose go.mod names a newer toolchain than this one gets it from the
# Go module proxy, checked against Go's checksum database.
export PATH="/usr/local/go/bin:${PATH}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
export GOFLAGS="${GOFLAGS:-}"
go version

# ---------------------------------------------------------------- build

log "Building dvpnd (this takes a few minutes the first time)"
# trust_source lets git read SRC, which the Makefile needs to stamp the version:
# git refuses a tree owned by another user. The entry replaces every entry for
# SRC (matched as text, not as a pattern), so a re-run adds nothing and the
# copies earlier versions of this installer added become one.
trust_source() {
  [[ -d "${SRC}/.git" ]] || return 0
  git config --global --replace-all --fixed-value safe.directory "${SRC}" "${SRC}" >/dev/null 2>&1 || true
}
trust_source
( cd "${SRC}" && make build )
install -m 0755 "${SRC}/bin/dvpnd" /usr/local/bin/dvpnd
/usr/local/bin/dvpnd version

# ---------------------------------------------------------------- protocol binaries

need_binary() { command -v "$1" >/dev/null; }

case "${NODE_TYPE}" in
  wireguard)
    need_binary wg || die "wg not found after installing wireguard-tools"
    ;;
  openvpn)
    need_binary openvpn || die "openvpn not found after installing the package"
    ;;
  v2ray)
    need_binary v2ray || die "v2ray (v5) is not on PATH: download v2ray-linux-64.zip from https://github.com/v2fly/v2ray-core/releases, unzip it and 'install -m 0755 v2ray /usr/local/bin/v2ray', then re-run"
    ;;
  xray)
    if ! need_binary xray; then
      [[ "${ARCH}" == "amd64" ]] || die "no pinned xray build for ${ARCH}: install xray ${XRAY_VERSION} on PATH yourself and re-run"
      log "Installing xray ${XRAY_VERSION}"
      curl -fsSL -o "${WORK}/xray.zip" "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VERSION}/Xray-linux-64.zip"
      echo "${XRAY_SHA256}  ${WORK}/xray.zip" | sha256sum -c - >/dev/null
      apt_get install -y -qq unzip >/dev/null
      unzip -q "${WORK}/xray.zip" xray -d "${WORK}/xray"
      install -m 0755 "${WORK}/xray/xray" /usr/local/bin/xray
    fi
    xray version | head -1
    ;;
  hysteria2)
    if ! need_binary hysteria; then
      [[ "${ARCH}" == "amd64" ]] || die "no pinned hysteria build for ${ARCH}: install hysteria ${HYSTERIA_VERSION} on PATH yourself and re-run"
      log "Installing hysteria ${HYSTERIA_VERSION}"
      curl -fsSL -o "${WORK}/hysteria" "https://github.com/apernet/hysteria/releases/download/${HYSTERIA_VERSION}/hysteria-linux-amd64"
      echo "${HYSTERIA_SHA256}  ${WORK}/hysteria" | sha256sum -c - >/dev/null
      install -m 0755 "${WORK}/hysteria" /usr/local/bin/hysteria
    fi
    hysteria version | head -1
    ;;
  amneziawg)
    if ! need_binary awg || ! need_binary awg-quick; then
      log "Building amneziawg-tools ${AWG_TOOLS_TAG}"
      git clone --quiet --branch "${AWG_TOOLS_TAG}" --depth 1 https://github.com/amnezia-vpn/amneziawg-tools.git "${WORK}/amneziawg-tools"
      git -C "${WORK}/amneziawg-tools" rev-parse HEAD | grep -q "^${AWG_TOOLS_COMMIT}" || die "amneziawg-tools tag does not match the pinned commit"
      make -C "${WORK}/amneziawg-tools/src" --jobs="$(nproc)" >/dev/null
      make -C "${WORK}/amneziawg-tools/src" PREFIX=/usr WITH_WGQUICK=yes WITH_BASHCOMPLETION=no WITH_SYSTEMDUNITS=no install >/dev/null
    fi
    if ! modinfo amneziawg >/dev/null 2>&1 && ! need_binary amneziawg-go; then
      log "Building amneziawg-go ${AWG_GO_TAG} (userspace data plane; the kernel module is optional and faster)"
      git clone --quiet --branch "${AWG_GO_TAG}" --depth 1 https://github.com/amnezia-vpn/amneziawg-go.git "${WORK}/amneziawg-go"
      git -C "${WORK}/amneziawg-go" rev-parse HEAD | grep -q "^${AWG_GO_COMMIT}" || die "amneziawg-go tag does not match the pinned commit"
      ( cd "${WORK}/amneziawg-go" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /usr/bin/amneziawg-go . )
    fi
    awg --version
    ;;
esac

# ---------------------------------------------------------------- network facts

log "Looking up the public address"
PUBLIC_IP=""
for url in https://api.ipify.org https://ipv4.icanhazip.com https://ifconfig.me; do
  PUBLIC_IP=$(curl -4 -fsS --max-time 10 "${url}" 2>/dev/null | tr -d '[:space:]') && [[ -n "${PUBLIC_IP}" ]] && break
done
[[ "${PUBLIC_IP}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "could not determine the public IPv4 address (is the host online?)"
LOCAL_IP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{ for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }')
BEHIND_NAT=0
[[ -n "${LOCAL_IP}" && "${LOCAL_IP}" != "${PUBLIC_IP}" ]] && BEHIND_NAT=1
echo "public IPv4: ${PUBLIC_IP}"
if [[ "${BEHIND_NAT}" -eq 1 ]]; then
  echo "local address: ${LOCAL_IP} (this host is behind a router or NAT; port forwarding is needed, see the end)"
  if [[ "${LOCAL_IP}" =~ ^100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\. ]]; then
    warn "the local address is in 100.64.0.0/10: this looks like carrier-grade NAT. Clients will not be able to reach the node."
  fi
fi

IPV6_OK=false
if ping -6 -c 1 -W 3 2606:4700:4700::1111 >/dev/null 2>&1; then IPV6_OK=true; fi
echo "IPv6 internet reachable: ${IPV6_OK}"

# ---------------------------------------------------------------- configuration

mkdir -p "${NODE_HOME}"
chmod 700 "${NODE_HOME}"
dv() {
  "${DVPND}" --home "${NODE_HOME}" "$@"
}

# The configuration and the protocol file are written in a scratch home inside
# the node's (dvs), and moved into place once every setting is in: a setting
# that fails stops the installer with the node's files as they were. Written in
# place, a file cut short was what the next run kept.
new_stage() {
  STAGE="${NODE_HOME}/.install"
  rm -rf "${STAGE}"
  mkdir -m 0700 "${STAGE}"
}
dvs() {
  "${DVPND}" --home "${STAGE}" "$@"
}

write_config() {
  new_stage
  dvs config init >/dev/null
  dvs config set keyring.backend test >/dev/null
  dvs config set keyring.from "${KEY_NAME}" >/dev/null
  dvs config set handshake.enable false >/dev/null
  dvs config set node.type "${NODE_TYPE}" >/dev/null
  dvs config set node.moniker "${MONIKER}" >/dev/null
  dvs config set node.gigabyte_prices "${GIGABYTE_PRICES}" >/dev/null
  dvs config set node.hourly_prices "${HOURLY_PRICES}" >/dev/null
  dvs config set node.listen_on "0.0.0.0:${API_PORT}" >/dev/null
  dvs config set node.remote_url "https://${PUBLIC_IP}:${API_PORT}" >/dev/null
  mv "${STAGE}/config.toml" "${NODE_HOME}/config.toml"
  rm -rf "${STAGE}"
}

if [[ -f "${NODE_HOME}/config.toml" && "${FORCE}" -eq 0 ]]; then
  log "Keeping the existing ${NODE_HOME}/config.toml (pass --force to rewrite it)"
else
  log "Writing ${NODE_HOME}/config.toml"
  [[ -n "${MONIKER}" ]] || ask MONIKER "Public name of the node (4-32 characters)"
  [[ ${#MONIKER} -ge 4 && ${#MONIKER} -le 32 ]] || die "the moniker must be 4 to 32 characters"
  write_config
fi
# A granter switches an existing node to a hot key too (a migration).
if [[ -n "${GRANTER}" ]]; then
  OLD_KEY=$(awk -F '[="]' '{ gsub(/ /, "") } /^\[keyring\]/ { f = 1 } f && /^from/ { print $3; exit }' "${NODE_HOME}/config.toml")
  dv config set keyring.from "${KEY_NAME}" >/dev/null
  dv config set keyring.granter "${GRANTER}" >/dev/null
fi

# proto_file prints the protocol file's name in the node's home.
proto_file() {
  case "${NODE_TYPE}" in
    hysteria2) echo "hysteria.toml" ;;
    *) echo "${NODE_TYPE}.toml" ;;
  esac
}

write_protocol() {
  new_stage
  dvs "${NODE_TYPE}" config init >/dev/null
  case "${NODE_TYPE}" in
    wireguard|amneziawg|openvpn)
      dvs "${NODE_TYPE}" config set listen_port "${LISTEN_PORT}" >/dev/null
      dvs "${NODE_TYPE}" config set enable_ipv6 "${IPV6_OK}" >/dev/null
      ;;
    v2ray) dvs v2ray config set vmess.listen_port "${LISTEN_PORT}" >/dev/null ;;
    xray) dvs xray config set vless.listen_port "${LISTEN_PORT}" >/dev/null ;;
    hysteria2) dvs hysteria2 config set server.listen_port "${LISTEN_PORT}" >/dev/null ;;
  esac
  if [[ "${NODE_TYPE}" == "amneziawg" ]]; then
    dvs amneziawg config set v3.listen_port "${V3_LISTEN_PORT}" >/dev/null
  fi
  if [[ "${NODE_TYPE}" == "openvpn" ]]; then
    dvs openvpn config set proto "${OPENVPN_PROTO}" >/dev/null
  fi
  mv "${STAGE}/$(proto_file)" "${NODE_HOME}/$(proto_file)"
  rm -rf "${STAGE}"
}

PROTO_FILE="${NODE_HOME}/$(proto_file)"
if [[ -f "${PROTO_FILE}" && "${FORCE}" -eq 0 ]]; then
  log "Keeping the existing ${PROTO_FILE} (pass --force to rewrite it)"
else
  log "Writing ${PROTO_FILE}"
  [[ -n "${LISTEN_PORT}" ]] || LISTEN_PORT=$(random_port)
  if [[ "${NODE_TYPE}" == "amneziawg" ]]; then
    [[ -n "${V3_LISTEN_PORT}" ]] || V3_LISTEN_PORT=$(random_port)
    [[ "${V3_LISTEN_PORT}" != "${LISTEN_PORT}" ]] || V3_LISTEN_PORT=$((LISTEN_PORT + 1))
  fi
  write_protocol
fi

# Read back what is actually in the files, whether written now or kept.
API_PORT=$(awk -F '[=":]' '{ gsub(/ /, "") } /^\[node\]/ { f = 1 } f && /^listen_on/ { print $4; exit }' "${NODE_HOME}/config.toml")
NODE_TYPE=$(awk -F '[="]' '{ gsub(/ /, "") } /^\[node\]/ { f = 1 } f && /^type/ { print $3; exit }' "${NODE_HOME}/config.toml")
PROTO_FILE="${NODE_HOME}/$(proto_file)"
case "${NODE_TYPE}" in
  wireguard|amneziawg|openvpn)
    LISTEN_PORT=$(awk -F '=' '{ gsub(/ /, "") } /^listen_port/ { print $2; exit }' "${PROTO_FILE}") ;;
  v2ray) LISTEN_PORT=$(awk -F '=' '{ gsub(/ /, "") } /^\[vmess\]/ { f = 1 } f && /^listen_port/ { print $2; exit }' "${PROTO_FILE}") ;;
  xray) LISTEN_PORT=$(awk -F '=' '{ gsub(/ /, "") } /^\[vless\]/ { f = 1 } f && /^listen_port/ { print $2; exit }' "${PROTO_FILE}") ;;
  hysteria2) LISTEN_PORT=$(awk -F '=' '{ gsub(/ /, "") } /^\[server\]/ { f = 1 } f && /^listen_port/ { print $2; exit }' "${PROTO_FILE}") ;;
esac
LISTEN_PROTO="udp"
case "${NODE_TYPE}" in
  v2ray|xray) LISTEN_PROTO="tcp" ;;
  openvpn) LISTEN_PROTO=$(awk -F '[="]' '{ gsub(/ /, "") } /^proto/ { print $3; exit }' "${PROTO_FILE}") ;;
esac
V3_ENABLED=false
if [[ "${NODE_TYPE}" == "amneziawg" ]]; then
  V3_ENABLED=$(awk -F '=' '{ gsub(/ /, "") } /^\[v3\]/ { s = 1 } s && /^enabled/ { print $2; exit }' "${PROTO_FILE}")
  V3_LISTEN_PORT=$(awk -F '=' '{ gsub(/ /, "") } /^\[v3\]/ { s = 1 } s && /^listen_port/ { print $2; exit }' "${PROTO_FILE}")
fi

# ---------------------------------------------------------------- key

# `keys show` exits 0 even when the key is missing, so look for the key's row.
key_exists() {
  dv keys show "${KEY_NAME}" 2>/dev/null |
    awk -v k="${KEY_NAME}" 'NR == 2 && $1 == k { found = 1 } END { exit !found }'
}

# create_key creates the operator key. Its mnemonic, the wallet's only backup,
# is printed on a terminal only: run under cloud-init, through tee or from a CI
# job, the output goes to a log, which would keep it. There the mnemonic goes
# to a file only root can read, for the operator to copy and delete.
create_key() {
  log "Creating the operator key"
  if [[ -t 2 ]]; then
    dv keys add "${KEY_NAME}"
    echo
    echo "The mnemonic above is the only backup of the node's wallet. Write it down now;"
    echo "it is not shown again. The key itself is stored unencrypted under ${NODE_HOME}/keyring-test"
    echo "so that the node can sign transactions unattended: keep only working funds on it."
    if [[ "${YES}" -eq 0 ]]; then
      read -r -p "Press Enter once the mnemonic is saved... " _
    fi
    return
  fi
  local backup="${NODE_HOME}/${KEY_NAME}-mnemonic.txt"
  [[ ! -e "${backup}" ]] || die "${backup} exists: copy the mnemonic in it somewhere safe, delete the file, and run the installer again"
  ( umask 077 && dv keys add "${KEY_NAME}" 2>"${backup}" ) || die "could not create the key '${KEY_NAME}' (see ${backup})"
  echo
  echo "This output is not a terminal, so the mnemonic, the only backup of the node's"
  echo "wallet, is not shown: it is in ${backup}, readable by root only."
  echo "Copy it somewhere safe, off this server, then delete the file:"
  echo "  shred -u ${backup}"
  echo "The key itself is stored unencrypted under ${NODE_HOME}/keyring-test so that the node"
  echo "can sign transactions unattended: keep only working funds on it."
}

if key_exists; then
  log "Keeping the existing key '${KEY_NAME}'"
elif [[ -n "${GRANTER}" ]]; then
  log "Creating the hot key"
  dv keys add "${KEY_NAME}" >/dev/null
  echo "The hot key only signs for the node account. It holds no funds and needs no backup:"
  echo "if it is lost, create another and grant it again."
else
  if [[ "${RECOVER}" -eq 1 ]]; then
    log "Importing the operator key: paste the mnemonic when asked"
    dv keys add "${KEY_NAME}" --recover
  else
    create_key
  fi
fi

# ---------------------------------------------------------------- TLS

if [[ -f "${NODE_HOME}/tls.crt" && -f "${NODE_HOME}/tls.key" ]]; then
  log "Keeping the existing TLS certificate"
else
  log "Creating a self-signed TLS certificate (clients pin it per session)"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -subj "/CN=dvpnd" -keyout "${NODE_HOME}/tls.key" -out "${NODE_HOME}/tls.crt" 2>/dev/null
fi
chmod 600 "${NODE_HOME}/tls.key"

# ---------------------------------------------------------------- firewall

# ssh_ports prints the TCP ports sshd listens on, one per line: as sshd reads
# its own configuration, drop-in files included; else from the files; else 22.
# Read from the main file's first Port line, a port set in sshd_config.d or by
# ListenAddress was missed, and enabling ufw shut the operator out.
ssh_ports() {
  local ports
  ports=$(sshd -T 2>/dev/null | awk '$1 == "listenaddress" { n = split($2, a, ":"); print a[n] }' | sort -un) || true
  if [[ -z "${ports}" ]]; then
    ports=$(cat "${SSHD_CONFIG}" "${SSHD_CONFIG}.d"/*.conf 2>/dev/null |
      awk 'tolower($1) == "port" { print $2 }
           tolower($1) == "listenaddress" && $2 ~ /:[0-9]+$/ { n = split($2, a, ":"); print a[n] }' | sort -un) || true
  fi
  printf '%s\n' "${ports:-22}"
}

if [[ "${FIREWALL}" -eq 1 ]]; then
  log "Opening the firewall (ufw)"
  apt_get install -y -qq ufw >/dev/null
  while read -r port; do
    ufw allow "${port}/tcp" >/dev/null
  done < <(ssh_ports)
  ufw allow "${API_PORT}/tcp" >/dev/null
  ufw allow "${LISTEN_PORT}/${LISTEN_PROTO}" >/dev/null
  [[ "${V3_ENABLED}" == "true" && -n "${V3_LISTEN_PORT}" ]] && ufw allow "${V3_LISTEN_PORT}/udp" >/dev/null
  if ! ufw --force enable >/dev/null 2>&1; then
    warn "ufw could not be enabled (no netfilter access?); open the ports listed below yourself"
  fi
  ufw status | sed 's/^/  /'
fi

# ---------------------------------------------------------------- proxy account

# The node runs the proxy daemons as this account, and OpenVPN drops to it
# once its tunnel is up; without it they run as root.
if ! id dvpnd-proxy >/dev/null 2>&1; then
  log "Creating the dvpnd-proxy system account"
  useradd --system --user-group --no-create-home --home-dir /nonexistent \
    --shell /usr/sbin/nologin dvpnd-proxy
fi

# ---------------------------------------------------------------- service

log "Installing the systemd unit"
sed -e "s#/root/.dvpnd#${NODE_HOME}#g" "${SRC}/scripts/dvpnd.service" >/etc/systemd/system/dvpnd.service
if [[ -d /run/systemd/system ]]; then
  systemctl daemon-reload
  if [[ "${START}" -eq 1 ]]; then
    systemctl enable --now dvpnd >/dev/null 2>&1 || systemctl enable dvpnd
    systemctl restart dvpnd
    sleep 2
    systemctl --no-pager --lines=0 status dvpnd || true
  else
    systemctl enable dvpnd >/dev/null 2>&1 || true
    echo "not started (--no-start): sudo systemctl start dvpnd"
  fi
else
  warn "systemd is not running here; the unit is installed at /etc/systemd/system/dvpnd.service but was not enabled"
fi

# ---------------------------------------------------------------- summary

# `keys show` prints Name, Address (sentnode1...), Operator (sent1...).
read -r _ NODE_ADDR OPERATOR_ADDR < <(dv keys show "${KEY_NAME}" | awk 'NR == 2')
if [[ -n "${GRANTER}" ]]; then
  HOT_ADDR="${OPERATOR_ADDR}"
  OPERATOR_ADDR="${GRANTER}"
  NODE_ADDR=$(dv keys authz-commands | awk '/^# Node address:/ { print $4; exit }')
fi

cat <<EOF

================================================================================
dvpnd is installed.

  node type        ${NODE_TYPE}
  node API         https://${PUBLIC_IP}:${API_PORT}   (tcp)
  ${NODE_TYPE} port   ${LISTEN_PORT}/${LISTEN_PROTO}
EOF
[[ "${V3_ENABLED}" == "true" ]] && echo "  AmneziaWG 3.1 port ${V3_LISTEN_PORT}/udp"
cat <<EOF
  operator wallet  ${OPERATOR_ADDR}
  node address     ${NODE_ADDR}
  home directory   ${NODE_HOME}

EOF
if [[ -n "${GRANTER}" ]]; then
  cat <<EOF
  hot key          ${HOT_ADDR}

What to do now:

1. From the wallet that holds the node account ${GRANTER},
   not on this server, make the grants the node needs (the node account pays every
   fee; keep a few P2P on it, 50 is a comfortable start):

EOF
  dv keys authz-commands | sed 's/^/   /'
  cat <<EOF

   The node refuses to start until the grants exist; it retries every 15 seconds,
   so it comes up by itself once they are made.
EOF
  if [[ -n "${OLD_KEY:-}" && "${OLD_KEY}" != "${KEY_NAME}" ]] && dv keys show "${OLD_KEY}" >/dev/null 2>&1; then
    cat <<EOF
   Once it runs on the hot key, delete the old key from this server, after
   making sure you hold its mnemonic elsewhere:
     dvpnd --home ${NODE_HOME} keys delete ${OLD_KEY}
EOF
  fi
else
  cat <<EOF

What to do now:

1. Send a few P2P (the coin formerly called DVPN) to the operator wallet above,
   50 is a comfortable start. Every status update and usage
   report is a transaction that costs gas; the node cannot register until the
   wallet holds some. Until then it retries and the log shows the error.
EOF
fi
# Step 2 exists only behind a router; the steps after it follow on from 1 or 2.
step=2
if [[ "${BEHIND_NAT}" -eq 1 ]]; then
  step=3
  cat <<EOF

2. On your router, forward these ports to ${LOCAL_IP} (this machine):
     ${API_PORT}/tcp
     ${LISTEN_PORT}/${LISTEN_PROTO}
EOF
  [[ "${V3_ENABLED}" == "true" ]] && echo "     ${V3_LISTEN_PORT}/udp"
  cat <<EOF
   Give this machine a fixed LAN address (DHCP reservation) so the rules keep
   working. If your provider uses carrier-grade NAT (router WAN address inside
   100.64.0.0/10), clients cannot reach the node no matter what: ask the
   provider for a public IPv4 address or run the node on a VPS.
EOF
fi
cat <<EOF

${step}. Watch the log until you see the node register and go active:
     journalctl -u dvpnd -f

$((step + 1)). Check that the node answers from outside your network, for example from a
   phone on mobile data:
     https://${PUBLIC_IP}:${API_PORT}/status
   (the browser warns about the self-signed certificate; that is expected).
   Client apps show the node a few minutes after it is reachable.

Change prices, moniker or ports in ${NODE_HOME}/config.toml and
${PROTO_FILE}, then 'sudo systemctl restart dvpnd'.
Upgrade by re-running this script; it rebuilds the latest release and keeps
your configuration and key.
================================================================================
EOF
