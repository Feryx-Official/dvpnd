#!/usr/bin/env bash

set -Eeou pipefail

# The node's container, wrapping the commands of docs/operator.md, section 7.
CONTAINER_NAME=dvpnd
NODE_DIR="${HOME}/.dvpnd"
RELEASE_IMAGE=ghcr.io/feryx-official/dvpnd
NODE_IMAGE="${RELEASE_IMAGE}:latest"
DOCKER_DAEMON_CONFIG=/etc/docker/daemon.json

# toml_value FILE TABLE KEY prints KEY's value in [TABLE] of FILE, unquoted;
# TABLE "" is the top of the file, before any table. A key is matched whole,
# at the start of its line, in its own table only: matched anywhere after
# [node], "type" was found in a moniker holding the word, nothing started, and
# start exited 0.
function toml_value {
  awk -v table="${2}" -v key="${3}" '
    /^[[:space:]]*\[/ { current = $0; gsub(/[][[:space:]]/, "", current); next }
    current == table && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      value = $0
      sub(/^[^=]*=[[:space:]]*/, "", value)
      if (value ~ /^"/) {
        sub(/^"/, "", value)
        sub(/".*$/, "", value)
      } else {
        sub(/[[:space:]]*(#.*)?$/, "", value)
      }
      print value
      exit
    }' "${1}"
}

# container_state prints the node container's state (running, exited, ...), or
# nothing when there is none. The name is matched whole: docker ps --filter
# name= matches part of a name, so stop and remove acted on any container whose
# name held this one.
function container_state {
  docker container inspect --format '{{ .State.Status }}' "${CONTAINER_NAME}" 2>/dev/null || true
}

function stop {
  [[ "$(container_state)" == "running" ]] && docker stop "${CONTAINER_NAME}"
  return 0
}

function remove {
  [[ -n "$(container_state)" ]] && docker rm --force --volumes "${CONTAINER_NAME}"
  return 0
}

# pull_image pulls NODE_IMAGE and, when cosign is installed, checks that this
# repository's release workflow built and signed it, as the operator guide does
# (section 7). An image that fails the check is removed; the node only runs
# an image already pulled (docker run --pull never), so it is not fetched
# again unchecked. An image of another name (an own build) is not checked.
function pull_image {
  docker pull "${NODE_IMAGE}" || return 1
  [[ "${NODE_IMAGE}" == "${RELEASE_IMAGE}:"* ]] || return 0
  if ! command -v cosign &>/dev/null; then
    echo "Warning: cosign is not installed, so ${NODE_IMAGE} is not checked against the signature"
    echo "of the repository's release workflow; install cosign 3 or later to check it (docs/operator.md, section 7)"
    return 0
  fi
  local digest
  digest=$(docker image inspect --format '{{ range .RepoDigests }}{{ println . }}{{ end }}' "${NODE_IMAGE}" |
    grep -m 1 "^${RELEASE_IMAGE}@") || true
  if [[ -z "${digest}" ]] || ! cosign verify "${digest}" \
    --certificate-identity-regexp '^https://github.com/Feryx-Official/dvpnd/\.github/workflows/docker-publish\.yml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null; then
    docker image rm "${NODE_IMAGE}" >/dev/null 2>&1 || true
    echo "Error: ${NODE_IMAGE} is not signed by the repository's release workflow; it is removed"
    return 1
  fi
  echo "Checked: ${digest} is signed by the repository's release workflow"
}

function cmd_attach {
  [[ "$(container_state)" == "running" ]] && docker attach "${CONTAINER_NAME}" && return 0
  echo "Error: node is not running" && return 1
}

function cmd_help {
  echo "Usage: ${0} [COMMAND]"
  echo ""
  echo "Commands:"
  echo "  attach     Attach to the already running node"
  echo "  help       Print the help message"
  echo "  init       Initialize the configuration"
  echo "  setup      Install the dependencies and set up the requirements"
  echo "  start      Start the node"
  echo "  status     Display the node's status along with the last 20 lines of the log"
  echo "  stop       Stop the node"
  echo "  remove     Remove the node container"
  echo "  restart    Restart the node"
  echo "  update     Pull the latest image and remove the node container; start it again after"
}

function cmd_init {
  NODE_TYPE=wireguard
  mapfile -t PORTS < <(shuf -i 1024-65535 -n 2)

  function run {
    docker run \
      --interactive \
      --pull never \
      --rm \
      --tty \
      --volume "${NODE_DIR}:/root/.dvpnd" \
      "${NODE_IMAGE}" process "${@}"
  }

  function must_run {
    output=$(run "${@}")
    [[ -n "${output}" ]] && echo "${output}"
    [[ "${output}" == *"Error"* ]] && return 1
    return 0
  }

  function config_set {
    echo "Setting the configuration key=${1}, value=${2}"
    must_run config set "${1}" "${2}"
  }

  function v2ray_config_set {
    echo "Setting the V2Ray configuration key=${1}, value=${2}"
    must_run v2ray config set "${1}" "${2}"
  }

  function xray_config_set {
    echo "Setting the XRAY configuration key=${1}, value=${2}"
    must_run xray config set "${1}" "${2}"
  }

  function hysteria2_config_set {
    echo "Setting the Hysteria2 configuration key=${1}, value=${2}"
    must_run hysteria2 config set "${1}" "${2}"
  }

  function amneziawg_config_set {
    echo "Setting the AmneziaWG configuration key=${1}, value=${2}"
    must_run amneziawg config set "${1}" "${2}"
  }

  function openvpn_config_set {
    echo "Setting the OpenVPN configuration key=${1}, value=${2}"
    must_run openvpn config set "${1}" "${2}"
  }

  function wireguard_config_set {
    echo "Setting the WireGuard configuration key=${1}, value=${2}"
    must_run wireguard config set "${1}" "${2}"
  }

  function cmd_init_config {
    function generate_moniker {
      id=$(docker create --pull never "${NODE_IMAGE}") &&
        name=$(docker inspect --format "{{ .Name }}" "${id}" | cut -c 2-) &&
        docker rm --force --volumes "${id}" >/dev/null 2>&1 &&
        echo "${name}"
    }

    function cmd_help {
      echo "Usage: ${0} init config COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    local force=0

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    PUBLIC_IP=$(curl -fsSL https://ifconfig.me)

    local chain_rpc_addresses="https://sentinel-rpc.publicnode.com:443,https://sentinel-rpc.polkachu.com:443,https://rpc-sentinel.busurnode.com:443,https://rpc.sentineldao.com:443"
    local handshake_enable=false
    # The node signs unattended with the test backend, as the operator guide
    # sets it; with file it asks for the passphrase at every start, through
    # attach, and a container Docker restarts waits for it.
    local keyring_backend=test
    local node_ipv4_address=
    local node_listen_on="0.0.0.0:${PORTS[0]}"
    local node_moniker && node_moniker=$(generate_moniker)
    # The installer's default prices.
    local node_gigabyte_prices="40000000udvpn"
    local node_hourly_prices="97500000udvpn"
    local node_remote_url="https://${PUBLIC_IP}:${PORTS[0]}"
    local node_type="${NODE_TYPE}"

    echo "Initializing the configuration..."
    must_run config init --force="${force}"

    read -p "Enter chain_rpc_addresses [${chain_rpc_addresses}]:" -r input
    [[ -n "${input}" ]] && chain_rpc_addresses="${input}"
    config_set "chain.rpc_addresses" "${chain_rpc_addresses}"

    read -p "Enter handshake_enable [${handshake_enable}]:" -r input
    [[ -n "${input}" ]] && handshake_enable="${input}"
    config_set "handshake.enable" "${handshake_enable}"

    read -p "Enter keyring_backend (test or file) [${keyring_backend}]:" -r input
    [[ -n "${input}" ]] && keyring_backend="${input}"
    config_set "keyring.backend" "${keyring_backend}"

    read -p "Enter node_ipv4_address:" -r input
    [[ -n "${input}" ]] && node_ipv4_address="${input}"
    config_set "node.ipv4_address" "${node_ipv4_address}"

    read -p "Enter node_listen_on [${node_listen_on}]:" -r input
    [[ -n "${input}" ]] && node_listen_on="${input}"
    config_set "node.listen_on" "${node_listen_on}"

    read -p "Enter node_moniker [${node_moniker}]:" -r input
    [[ -n "${input}" ]] && node_moniker="${input}"
    config_set "node.moniker" "${node_moniker}"

    read -p "Enter node_gigabyte_prices [${node_gigabyte_prices}]:" -r input
    [[ -n "${input}" ]] && node_gigabyte_prices="${input}"
    config_set "node.gigabyte_prices" "${node_gigabyte_prices}"

    read -p "Enter node_hourly_prices [${node_hourly_prices}]:" -r input
    [[ -n "${input}" ]] && node_hourly_prices="${input}"
    config_set "node.hourly_prices" "${node_hourly_prices}"

    read -p "Enter node_remote_url [${node_remote_url}]:" -r input
    [[ -n "${input}" ]] && node_remote_url="${input}"
    config_set "node.remote_url" "${node_remote_url}"

    read -p "Enter node_type [${node_type}]:" -r input
    [[ -n "${input}" ]] && NODE_TYPE="${input}" && node_type="${input}"
    config_set "node.type" "${node_type}"
  }

  function cmd_init_keys {
    read -p "Recover the existing account? [skip]:" -r input
    if [[ "${input}" == "no" ]]; then
      run keys add
    elif [[ "${input}" == "yes" ]]; then
      run keys add --recover
    fi
  }

  function cmd_init_v2ray {
    function cmd_help {
      echo "Usage: ${0} init v2ray COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    local force=0

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    local listen_port=${PORTS[1]}
    local transport=grpc

    echo "Initializing the V2Ray configuration..."
    must_run v2ray config init --force="${force}"

    read -p "Enter vmess.listen_port [${listen_port}]:" -r input
    [[ -n "${input}" ]] && listen_port="${input}"
    v2ray_config_set "vmess.listen_port" "${listen_port}"

    read -p "Enter vmess.transport [${transport}]:" -r input
    [[ -n "${input}" ]] && transport="${input}"
    v2ray_config_set "vmess.transport" "${transport}"
  }

  function cmd_init_xray {
    function cmd_help {
      echo "Usage: ${0} init xray COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    local force=0

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    local listen_port=${PORTS[1]}
    local security=tls

    echo "Initializing the XRAY configuration..."
    must_run xray config init --force="${force}"

    read -p "Enter vless.listen_port [${listen_port}]:" -r input
    [[ -n "${input}" ]] && listen_port="${input}"
    xray_config_set "vless.listen_port" "${listen_port}"

    read -p "Enter vless.security (tls or reality) [${security}]:" -r input
    [[ -n "${input}" ]] && security="${input}"
    xray_config_set "vless.security" "${security}"
  }

  function cmd_init_hysteria2 {
    function cmd_help {
      echo "Usage: ${0} init hysteria2 COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    local force=0

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    local listen_port=${PORTS[1]}

    echo "Initializing the Hysteria2 configuration..."
    must_run hysteria2 config init --force="${force}"

    read -p "Enter server.listen_port [${listen_port}]:" -r input
    [[ -n "${input}" ]] && listen_port="${input}"
    hysteria2_config_set "server.listen_port" "${listen_port}"
  }

  function cmd_init_amneziawg {
    function cmd_help {
      echo "Usage: ${0} init amneziawg COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    local force=0

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    local listen_port=${PORTS[1]}

    echo "Initializing the AmneziaWG configuration..."
    must_run amneziawg config init --force="${force}"

    read -p "Enter listen_port [${listen_port}]:" -r input
    [[ -n "${input}" ]] && listen_port="${input}"
    amneziawg_config_set "listen_port" "${listen_port}"

    # The AmneziaWG 3.1 tier listens on its own port.
    local v3_listen_port
    v3_listen_port=$(shuf -i 1024-65535 -n 1)
    read -p "Enter v3.listen_port (AmneziaWG 3.1 tier) [${v3_listen_port}]:" -r input
    [[ -n "${input}" ]] && v3_listen_port="${input}"
    amneziawg_config_set "v3.listen_port" "${v3_listen_port}"
  }

  function cmd_init_openvpn {
    function cmd_help {
      echo "Usage: ${0} init openvpn COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    local force=0

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    local listen_port=${PORTS[1]}
    local proto=udp

    echo "Initializing the OpenVPN configuration..."
    must_run openvpn config init --force="${force}"

    read -p "Enter listen_port [${listen_port}]:" -r input
    [[ -n "${input}" ]] && listen_port="${input}"
    openvpn_config_set "listen_port" "${listen_port}"

    read -p "Enter proto (udp or tcp) [${proto}]:" -r input
    [[ -n "${input}" ]] && proto="${input}"
    openvpn_config_set "proto" "${proto}"
  }

  function cmd_init_wireguard {
    function cmd_help {
      echo "Usage: ${0} init wireguard COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    local force=0

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    local listen_port=${PORTS[1]}

    echo "Initializing the WireGuard configuration..."
    must_run wireguard config init --force="${force}"

    read -p "Enter listen_port [${listen_port}]:" -r input
    [[ -n "${input}" ]] && listen_port="${input}"
    wireguard_config_set "listen_port" "${listen_port}"
  }

  function cmd_init_all {
    function cmd_help {
      echo "Usage: ${0} init all COMMAND OPTIONS"
      echo ""
      echo "Commands:"
      echo "  help    Print the help message"
      echo ""
      echo "Options:"
      echo "  -f, --force    Force the initialization"
    }

    [[ "${#}" -gt 0 ]] && {
      case "${1}" in
        "-f" | "--force") force=1 ;;
        "help") cmd_help && return 0 ;;
        *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
      esac
    }

    cmd_init_config "${@}"
    [[ "${NODE_TYPE}" == "v2ray" ]] && cmd_init_v2ray "${@}"
    [[ "${NODE_TYPE}" == "xray" ]] && cmd_init_xray "${@}"
    [[ "${NODE_TYPE}" == "hysteria2" ]] && cmd_init_hysteria2 "${@}"
    [[ "${NODE_TYPE}" == "amneziawg" ]] && cmd_init_amneziawg "${@}"
    [[ "${NODE_TYPE}" == "openvpn" ]] && cmd_init_openvpn "${@}"
    [[ "${NODE_TYPE}" == "wireguard" ]] && cmd_init_wireguard "${@}"
    cmd_init_keys "${@}"
  }

  function cmd_init_help {
    echo "Usage: ${0} init [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  all          Initialize everything"
    echo "  amneziawg    Initialize the amneziawg.toml file"
    echo "  config       Initialize the config.toml file"
    echo "  help         Print the help message"
    echo "  hysteria2    Initialize the hysteria.toml file"
    echo "  keys         Initialize the keys"
    echo "  openvpn      Initialize the openvpn.toml file"
    echo "  v2ray        Initialize the v2ray.toml file"
    echo "  wireguard    Initialize the wireguard.toml file"
    echo "  xray         Initialize the xray.toml file"
  }

  v="${1:-help}" && case "${v}" in
    "all" | "amneziawg" | "config" | "help" | "hysteria2" | "keys" | "openvpn" | "v2ray" | "wireguard" | "xray")
      shift || true
      cmd_init_"${v}" "${@}"
      ;;
    *)
      echo "Error: invalid command or option \"${1}\"" && return 1
      ;;
  esac
}

function cmd_setup {
  [[ "$EUID" -ne 0 ]] &&
    echo "Error: please run this command with sudo privileges" &&
    return 1

  setup_host
}

function install_packages {
  echo "Installing the packages ${*}"
  DEBIAN_FRONTEND=noninteractive apt-get install --quiet --yes "${@}"
}

# setup_host prepares a Debian or Ubuntu host for the node container and keeps
# what the host already has. Docker must be installed already: setup ran
# get.docker.com's script as root, unchecked. An existing daemon.json and TLS
# certificate are left alone, and no package that replaces the firewall is
# installed: iptables-persistent, which setup installed, removes ufw.
function setup_host {
  if ! command -v docker &>/dev/null; then
    echo "Error: Docker is not installed. Install it from the distribution's packages or from"
    echo "Docker's signed apt repository (https://docs.docker.com/engine/install/), then run setup again"
    return 1
  fi
  apt-get update
  install_packages curl openssl
  setup_docker_ipv6
  setup_tls
  pull_image
}

# setup_docker_ipv6 gives containers IPv6 as the operator guide does (section
# 7): Docker NATs their IPv6 itself. It writes DOCKER_DAEMON_CONFIG and
# restarts Docker only when the host has no such file: rewriting it dropped
# the host's own settings, and the restart restarts every container. A Docker
# that does not start with the file has it removed again.
function setup_docker_ipv6 {
  if [[ -e "${DOCKER_DAEMON_CONFIG}" ]]; then
    if grep -Eq '"ipv6"[[:space:]]*:[[:space:]]*true' "${DOCKER_DAEMON_CONFIG}"; then
      echo "Keeping ${DOCKER_DAEMON_CONFIG}: it gives containers IPv6"
    else
      echo "Keeping ${DOCKER_DAEMON_CONFIG}, which does not give containers IPv6. For a dual-stack"
      echo "WireGuard node add \"ipv6\": true, \"fixed-cidr-v6\": \"fd00:d0c:1::/64\" and \"ip6tables\": true"
      echo "to it and restart Docker, or set enable_ipv6 = false in wireguard.toml (docs/operator.md, section 7)"
    fi
    return 0
  fi
  mkdir -p "$(dirname "${DOCKER_DAEMON_CONFIG}")"
  cat <<EOF >"${DOCKER_DAEMON_CONFIG}"
{
  "ipv6": true,
  "fixed-cidr-v6": "fd00:d0c:1::/64",
  "ip6tables": true
}
EOF
  if ! systemctl restart docker; then
    rm -f "${DOCKER_DAEMON_CONFIG}"
    systemctl restart docker
    echo "Error: Docker did not start with IPv6 on; ${DOCKER_DAEMON_CONFIG} is removed again"
    return 1
  fi
}

# setup_tls makes the node's self-signed certificate, as the installer does,
# unless NODE_DIR has one: clients pin it, and setup replaced it at every run.
function setup_tls {
  if [[ -f "${NODE_DIR}/tls.crt" && -f "${NODE_DIR}/tls.key" ]]; then
    echo "Keeping the TLS certificate in ${NODE_DIR}"
    return 0
  fi
  mkdir -p "${NODE_DIR}" && chmod 700 "${NODE_DIR}"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -subj "/CN=dvpnd" -keyout "${NODE_DIR}/tls.key" -out "${NODE_DIR}/tls.crt" 2>/dev/null
  chmod 600 "${NODE_DIR}/tls.key"
}

function cmd_start {
  function cmd_help {
    echo "Usage: ${0} start COMMAND OPTIONS"
    echo ""
    echo "Commands:"
    echo "  help    Print the help message"
    echo ""
    echo "Options:"
    echo "  -d, --detach    Start the node container detached"
  }

  local detach=0

  [[ "${#}" -gt 0 ]] && {
    case "${1}" in
      "-d" | "--detach") detach=1 ;;
      "help") cmd_help && return 0 ;;
      *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
    esac
  }

  local node_type api_port listen_port
  node_type=$(toml_value "${NODE_DIR}/config.toml" node type)
  api_port=$(toml_value "${NODE_DIR}/config.toml" node listen_on)
  api_port="${api_port##*:}"

  # run_node runs the node container with the flags every node type shares
  # and the type's own in "$@", as docs/operator.md, section 7, does.
  # Detached, Docker starts it again after a crash or a reboot; in the
  # foreground, it is removed when it stops. It runs the image setup or update
  # pulled and checked, never one fetched on the way.
  function run_node {
    local mode=(--rm)
    [[ "${detach}" -eq 1 ]] && mode=(--detach --restart unless-stopped)
    docker run "${mode[@]}" \
      --pull never \
      --interactive \
      --tty \
      --name "${CONTAINER_NAME}" \
      --log-opt max-size=50m \
      --log-opt max-file=3 \
      --volume "${NODE_DIR}:/root/.dvpnd" \
      --publish "${api_port}:${api_port}/tcp" \
      "${@}" \
      "${NODE_IMAGE}" process start
  }

  # IP forwarding for the tunnel types, set here because /proc/sys is
  # read-only inside the container.
  local forwarding=(
    --sysctl net.ipv4.ip_forward=1
    --sysctl net.ipv6.conf.all.disable_ipv6=0
    --sysctl net.ipv6.conf.all.forwarding=1
    --sysctl net.ipv6.conf.default.forwarding=1
  )

  case "${node_type}" in
    v2ray | xray | hysteria2)
      # Proxy node types run as a plain process on one port: no modules, no
      # sysctls. The node starts the proxy as the dvpnd-proxy account (SETUID,
      # SETGID; CHOWN for its runtime files; KILL to stop it) behind an OUTPUT
      # chain for that account (NET_ADMIN, NET_RAW); every other capability is
      # dropped.
      local file table proto=tcp
      case "${node_type}" in
        v2ray) file=v2ray.toml table=vmess ;;
        xray) file=xray.toml table=vless ;;
        hysteria2) file=hysteria.toml table=server proto=udp ;;
      esac
      listen_port=$(toml_value "${NODE_DIR}/${file}" "${table}" listen_port)
      run_node \
        --cap-drop ALL \
        --cap-add NET_BIND_SERVICE \
        --cap-add NET_ADMIN \
        --cap-add NET_RAW \
        --cap-add SETUID \
        --cap-add SETGID \
        --cap-add CHOWN \
        --cap-add KILL \
        --publish "${listen_port}:${listen_port}/${proto}"
      ;;
    amneziawg)
      # Userspace AmneziaWG on a tun device: no kernel module, so no SYS_MODULE
      # and no /lib/modules, but the tun device. The 3.1 tier ([v3]) has a port
      # of its own, published when it is enabled.
      listen_port=$(toml_value "${NODE_DIR}/amneziawg.toml" "" listen_port)
      local v3=()
      if [[ "$(toml_value "${NODE_DIR}/amneziawg.toml" v3 enabled)" == "true" ]]; then
        local v3_port
        v3_port=$(toml_value "${NODE_DIR}/amneziawg.toml" v3 listen_port)
        v3=(--publish "${v3_port}:${v3_port}/udp")
      fi
      run_node \
        --device /dev/net/tun \
        --cap-drop ALL \
        --cap-add NET_ADMIN \
        --cap-add NET_BIND_SERVICE \
        --cap-add NET_RAW \
        "${forwarding[@]}" \
        --publish "${listen_port}:${listen_port}/udp" \
        "${v3[@]}"
      ;;
    openvpn)
      # A tun device, NAT and forwarding, no kernel module. The server drops to
      # the dvpnd-proxy account once its tunnel is up (SETUID, SETGID; CHOWN
      # for its runtime files; KILL to stop it).
      local proto
      listen_port=$(toml_value "${NODE_DIR}/openvpn.toml" "" listen_port)
      proto=$(toml_value "${NODE_DIR}/openvpn.toml" "" proto)
      run_node \
        --device /dev/net/tun \
        --cap-drop ALL \
        --cap-add NET_ADMIN \
        --cap-add NET_BIND_SERVICE \
        --cap-add NET_RAW \
        --cap-add SETUID \
        --cap-add SETGID \
        --cap-add CHOWN \
        --cap-add KILL \
        "${forwarding[@]}" \
        --publish "${listen_port}:${listen_port}/${proto:-udp}"
      ;;
    wireguard)
      listen_port=$(toml_value "${NODE_DIR}/wireguard.toml" "" listen_port)
      run_node \
        --volume /lib/modules:/lib/modules:ro \
        --cap-drop ALL \
        --cap-add NET_ADMIN \
        --cap-add NET_BIND_SERVICE \
        --cap-add NET_RAW \
        --cap-add SYS_MODULE \
        "${forwarding[@]}" \
        --publish "${listen_port}:${listen_port}/udp"
      ;;
    *)
      echo "Error: ${NODE_DIR}/config.toml names no node type the runner knows (\"${node_type}\")" && return 1
      ;;
  esac
}

function cmd_status {
  local state
  state=$(container_state)
  if [[ -z "${state}" ]]; then
    echo "Error: node container does not exist" && return 1
  fi
  if [[ "${state}" == "running" ]]; then
    echo "Node is running..."
  else
    exit_code=$(docker container inspect --format "{{ .State.ExitCode }}" "${CONTAINER_NAME}")
    echo "Node is not running and has exited with code ${exit_code}"
  fi
  docker logs --tail 20 "${CONTAINER_NAME}"
}

function cmd_stop {
  [[ "$(container_state)" == "running" ]] && docker stop "${CONTAINER_NAME}" && return 0
  echo "Error: node is not running" && return 1
}

function cmd_remove {
  [[ -n "$(container_state)" ]] && docker rm --force --volumes "${CONTAINER_NAME}" && return 0
  echo "Error: node container does not exist" && return 1
}

function cmd_restart {
  function cmd_help {
    echo "Usage: ${0} restart COMMAND OPTIONS"
    echo ""
    echo "Commands:"
    echo "  help    Print the help message"
    echo ""
    echo "Options:"
    echo "  -d, --detach    Start the node container detached"
  }

  [[ "${#}" -gt 0 ]] && {
    case "${1}" in
      "-d" | "--detach") ;;
      "help") cmd_help && return 0 ;;
      *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
    esac
  }

  stop
  remove
  cmd_start "${@}"
}

# cmd_update pulls and checks the image first, so an image that fails the
# check leaves the running node as it is.
function cmd_update {
  pull_image
  stop
  remove
  echo "Start the node on the new image: ${0} start --detach"
}

# Sourced, as the tests do, the script defines its functions and runs nothing.
[[ "${BASH_SOURCE[0]}" == "${0}" ]] || return 0

v="${1:-help}" && case "${v}" in
  "attach" | "help" | "init" | "setup" | "start" | "status" | "stop" | "remove" | "restart" | "update")
    shift || true
    cmd_"${v}" "${@}"
    ;;
  *)
    echo "Error: invalid command or option \"${1}\"" && exit 1
    ;;
esac
