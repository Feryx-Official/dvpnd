#!/usr/bin/env bash

set -Eeou pipefail

CONTAINER_NAME=dvpnd
NODE_DIR="${HOME}/.dvpnd"
NODE_IMAGE=ghcr.io/feryx-official/dvpnd:latest

function stop {
  id=$(docker ps --filter name="${CONTAINER_NAME}" --quiet)
  [[ -n "${id}" ]] && docker stop "${id}"
  return 0
}

function remove {
  id=$(docker ps --all --filter name="${CONTAINER_NAME}" --quiet)
  [[ -n "${id}" ]] && docker rm --force --volumes "${id}"
  return 0
}

function cmd_attach {
  id=$(docker ps --filter name="${CONTAINER_NAME}" --quiet)
  [[ -n "${id}" ]] && docker attach "${id}" && return 0
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
  echo "  update     Update the node to the latest version"
}

function cmd_init {
  NODE_TYPE=wireguard
  mapfile -t PORTS < <(shuf -i 1024-65535 -n 2)

  function run {
    docker run \
      --interactive \
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
      id=$(docker create "${NODE_IMAGE}") &&
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
    local keyring_backend=file
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

    read -p "Enter keyring_backend [${keyring_backend}]:" -r input
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

  function install_packages {
    echo "Installing the packages ${*}"
    DEBIAN_FRONTEND=noninteractive apt-get install --quiet --yes "${@}"
  }

  function setup_docker {
    function install {
      if ! command -v docker &>/dev/null; then
        curl -fsSL -o /tmp/get-docker.sh https://get.docker.com
        sh /tmp/get-docker.sh
      fi
    }

    function setup_ipv6 {
      cat <<EOF >/etc/docker/daemon.json
{
    "ipv6": true,
    "fixed-cidr-v6": "2001:db8:1::/64"
}
EOF
    }

    install
    setup_ipv6
    systemctl restart docker
  }

  function setup_iptables {
    install_packages iptables-persistent
    rule=(POSTROUTING -s 2001:db8:1::/64 ! -o docker0 -j MASQUERADE)
    ip6tables -t nat -C "${rule[@]}" 2>/dev/null || ip6tables -t nat -A "${rule[@]}"
    ip6tables-save >/etc/iptables/rules.v6
  }

  function setup_tls {
    mkdir -p "${NODE_DIR}" &&
      openssl req -new \
        -newkey ec \
        -pkeyopt ec_paramgen_curve:prime256v1 \
        -subj "/C=NA/ST=NA/L=./O=NA/OU=./CN=." \
        -x509 \
        -sha256 \
        -days 365 \
        -nodes \
        -out "${NODE_DIR}/tls.crt" \
        -keyout "${NODE_DIR}/tls.key"
  }

  apt-get update
  install_packages curl git jq openssl
  setup_docker
  setup_iptables
  setup_tls
  docker pull "${NODE_IMAGE}"
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
  local rm=1

  [[ "${#}" -gt 0 ]] && {
    case "${1}" in
      "-d" | "--detach") detach=1 && rm=0 ;;
      "help") cmd_help && return 0 ;;
      *) echo "Error: invalid command or option \"${1}\"" && return 1 ;;
    esac
  }

  local node_api_port=
  local node_type=

  node_api_port=$(awk -F '[=":]' '{gsub(/ /,"")} /\[node\]/{f=1} f && /listen_on/{print $4;exit}' "${NODE_DIR}/config.toml")
  node_type=$(awk -F '[="]' '{gsub(/ /,"")} /\[node\]/{f=1} f && /type/{print $3;exit}' "${NODE_DIR}/config.toml")

  # Proxy node types run as a plain process on one port: no modules, no
  # sysctls. The node starts the proxy as the dvpnd-proxy account (SETUID,
  # SETGID; CHOWN for its runtime files; KILL to stop it) behind an OUTPUT
  # chain for that account (NET_ADMIN, NET_RAW); every other capability is
  # dropped.
  local proxy_port= proxy_proto=tcp
  [[ "${node_type}" == "v2ray" ]] &&
    proxy_port=$(awk -F '=' '{gsub(/ /,"")} /\[vmess\]/{f=1} f && /listen_port/{print $2;exit}' "${NODE_DIR}/v2ray.toml")
  [[ "${node_type}" == "xray" ]] &&
    proxy_port=$(awk -F '=' '{gsub(/ /,"")} /\[vless\]/{f=1} f && /listen_port/{print $2;exit}' "${NODE_DIR}/xray.toml")
  [[ "${node_type}" == "hysteria2" ]] && proxy_proto=udp &&
    proxy_port=$(awk -F '=' '{gsub(/ /,"")} /\[server\]/{f=1} f && /listen_port/{print $2;exit}' "${NODE_DIR}/hysteria.toml")
  if [[ -n "${proxy_port}" ]]; then
    docker run \
      --detach="${detach}" \
      --interactive \
      --name="${CONTAINER_NAME}" \
      --rm="${rm}" \
      --tty \
      --volume "${NODE_DIR}:/root/.dvpnd" \
      --cap-drop ALL \
      --cap-add NET_BIND_SERVICE \
      --cap-add NET_ADMIN \
      --cap-add NET_RAW \
      --cap-add SETUID \
      --cap-add SETGID \
      --cap-add CHOWN \
      --cap-add KILL \
      --publish "${node_api_port}:${node_api_port}/tcp" \
      --publish "${proxy_port}:${proxy_port}/${proxy_proto}" \
      "${NODE_IMAGE}" process start
  fi
  if [[ "${node_type}" == "amneziawg" ]]; then
    # Userspace AmneziaWG on a tun device: no kernel module, so no SYS_MODULE
    # and no /lib/modules, but the tun device.
    port=$(awk -F '=' '{gsub(/ /,"")} /listen_port/{print $2;exit}' "${NODE_DIR}/amneziawg.toml")
    # The AmneziaWG 3.1 tier ([v3]) has a port of its own, published when enabled.
    v3_enabled=$(awk -F '=' '{gsub(/ /,"")} /^\[v3\]/{s=1} s&&/^enabled/{print $2;exit}' "${NODE_DIR}/amneziawg.toml")
    v3_port=$(awk -F '=' '{gsub(/ /,"")} /^\[v3\]/{s=1} s&&/^listen_port/{print $2;exit}' "${NODE_DIR}/amneziawg.toml")
    v3_publish=""
    [[ "${v3_enabled}" == "true" && -n "${v3_port}" ]] && v3_publish="--publish ${v3_port}:${v3_port}/udp"
    # shellcheck disable=SC2086
    docker run \
      --detach="${detach}" \
      --interactive \
      --name="${CONTAINER_NAME}" \
      --rm="${rm}" \
      --tty \
      --device /dev/net/tun \
      --volume "${NODE_DIR}:/root/.dvpnd" \
      --cap-drop ALL \
      --cap-add NET_ADMIN \
      --cap-add NET_BIND_SERVICE \
      --cap-add NET_RAW \
      --sysctl net.ipv4.ip_forward=1 \
      --sysctl net.ipv6.conf.all.disable_ipv6=0 \
      --sysctl net.ipv6.conf.all.forwarding=1 \
      --sysctl net.ipv6.conf.default.forwarding=1 \
      --publish "${node_api_port}:${node_api_port}/tcp" \
      --publish "${port}:${port}/udp" \
      ${v3_publish} \
      "${NODE_IMAGE}" process start
  fi
  if [[ "${node_type}" == "openvpn" ]]; then
    # A tun device, NAT and forwarding, no kernel module. The server drops to
    # the dvpnd-proxy account once its tunnel is up (SETUID, SETGID; CHOWN
    # for its runtime files; KILL to stop it).
    port=$(awk -F '=' '{gsub(/ /,"")} /listen_port/{print $2;exit}' "${NODE_DIR}/openvpn.toml")
    proto=$(awk -F '[="]' '{gsub(/ /,"")} /^proto/{print $3;exit}' "${NODE_DIR}/openvpn.toml")
    docker run \
      --detach="${detach}" \
      --interactive \
      --name="${CONTAINER_NAME}" \
      --rm="${rm}" \
      --tty \
      --device /dev/net/tun \
      --volume "${NODE_DIR}:/root/.dvpnd" \
      --cap-drop ALL \
      --cap-add NET_ADMIN \
      --cap-add NET_BIND_SERVICE \
      --cap-add NET_RAW \
      --cap-add SETUID \
      --cap-add SETGID \
      --cap-add CHOWN \
      --cap-add KILL \
      --sysctl net.ipv4.ip_forward=1 \
      --sysctl net.ipv6.conf.all.disable_ipv6=0 \
      --sysctl net.ipv6.conf.all.forwarding=1 \
      --sysctl net.ipv6.conf.default.forwarding=1 \
      --publish "${node_api_port}:${node_api_port}/tcp" \
      --publish "${port}:${port}/${proto:-udp}" \
      "${NODE_IMAGE}" process start
  fi
  if [[ "${node_type}" == "wireguard" ]]; then
    port=$(awk -F '=' '{gsub(/ /,"")} /listen_port/{print $2;exit}' "${NODE_DIR}/wireguard.toml")
    docker run \
      --detach="${detach}" \
      --interactive \
      --name="${CONTAINER_NAME}" \
      --rm="${rm}" \
      --tty \
      --volume /lib/modules:/lib/modules \
      --volume "${NODE_DIR}:/root/.dvpnd" \
      --cap-drop ALL \
      --cap-add NET_ADMIN \
      --cap-add NET_BIND_SERVICE \
      --cap-add NET_RAW \
      --cap-add SYS_MODULE \
      --sysctl net.ipv4.ip_forward=1 \
      --sysctl net.ipv6.conf.all.disable_ipv6=0 \
      --sysctl net.ipv6.conf.all.forwarding=1 \
      --sysctl net.ipv6.conf.default.forwarding=1 \
      --publish "${node_api_port}:${node_api_port}/tcp" \
      --publish "${port}:${port}/udp" \
      "${NODE_IMAGE}" process start
  fi
}

function cmd_status {
  id=$(docker ps --all --filter name="${CONTAINER_NAME}" --quiet)
  if [[ -n "${id}" ]]; then
    running=$(docker container inspect --format "{{ .State.Running }}" "${CONTAINER_NAME}")
    if [[ "${running}" == "true" ]]; then
      echo "Node is running..."
    else
      exit_code=$(docker container inspect --format "{{ .State.ExitCode }}" "${CONTAINER_NAME}")
      echo "Node is not running and has exited with code ${exit_code}"
    fi
    docker logs --tail 20 "${CONTAINER_NAME}"
  else
    echo "Error: node container does not exist" && return 1
  fi
}

function cmd_stop {
  id=$(docker ps --filter name="${CONTAINER_NAME}" --quiet)
  [[ -n "${id}" ]] && docker stop "${id}" && return 0
  echo "Error: node is not running" && return 1
}

function cmd_remove {
  id=$(docker ps --all --filter name="${CONTAINER_NAME}" --quiet)
  [[ -n "${id}" ]] && docker rm --force --volumes "${id}" && return 0
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

function cmd_update {
  stop
  remove
  docker rmi --force "${NODE_IMAGE}"
  docker pull "${NODE_IMAGE}"
}

v="${1:-help}" && case "${v}" in
  "attach" | "help" | "init" | "setup" | "start" | "status" | "stop" | "remove" | "restart" | "update")
    shift || true
    cmd_"${v}" "${@}"
    ;;
  *)
    echo "Error: invalid command or option \"${1}\"" && exit 1
    ;;
esac
