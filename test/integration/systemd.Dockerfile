# A Debian host running systemd, for the integration suite's systemd stage
# (test/integration/run.sh): the node's unit runs the tests there. XRAY and
# Hysteria2 are the static binaries of the node image; OpenVPN and iptables
# are Debian's, as on a VPS.
ARG NODE_IMAGE=dvpnd:it
FROM ${NODE_IMAGE} AS node

FROM debian:trixie@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6
RUN apt-get update && \
    apt-get install -y --no-install-recommends systemd systemd-sysv iptables iproute2 openvpn procps ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin dvpnd-proxy
COPY --from=node /usr/local/bin/xray /usr/local/bin/hysteria /usr/local/bin/
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
