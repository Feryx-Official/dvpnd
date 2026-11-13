# SPDX-License-Identifier: Apache-2.0
# Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.
# Base images are pinned by digest (Dependabot moves the pins), so a tag
# re-pointed upstream cannot change what is built. Both stages are the same
# Alpine release, since hnsd is built against the libraries the image runs
# it with, and the Go toolchain is the one go.mod names and the installer
# installs.
FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build

# Proxy binaries the node drives, pinned to the releases the client apps are
# tested against and checked against the sha256 the project publishes. The
# installer pins the same (scripts/install.sh); a test holds the two together.
ARG XRAY_VERSION=v26.3.27
ARG XRAY_SHA256=23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae
ARG HYSTERIA_VERSION=app/v2.10.0
ARG HYSTERIA_SHA256=04f7804159ef1d798de12a817d73aab4b9040ebe45fc62e223000c5c59e987fe
# AmneziaWG userspace implementation and tools, built from source at the
# AmneziaWG 3.1 tags (the kernel module cannot ship in an image). Newer than
# what the client apps bundle: the default tier never sets a 3.x parameter, and
# with those unset the 3.1 engine is byte-for-byte the 2.0 one on the wire, so
# every current app still connects; the 3.1 tier is only handed to a client
# that asks for it (docs/protocols.md).
ARG AWG_GO_TAG=v3.1.20260828
ARG AWG_GO_COMMIT=b5928efb6ca19f0153958460c3d141f04abc5c2e
ARG AWG_TOOLS_TAG=v3.1.20260812
ARG AWG_TOOLS_COMMIT=ee0f0a9aa34ff0a0da4b3433b9512781cfe02843
RUN apk add --no-cache unzip && \
    wget -qO /tmp/xray.zip "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VERSION}/Xray-linux-64.zip" && \
    echo "${XRAY_SHA256}  /tmp/xray.zip" | sha256sum -c - && \
    unzip -q /tmp/xray.zip xray -d /tmp/xray && chmod 0755 /tmp/xray/xray && \
    wget -qO /tmp/hysteria "https://github.com/apernet/hysteria/releases/download/${HYSTERIA_VERSION}/hysteria-linux-amd64" && \
    echo "${HYSTERIA_SHA256}  /tmp/hysteria" | sha256sum -c - && chmod 0755 /tmp/hysteria

COPY . /root/dvpnd/

RUN --mount=target=/go/pkg/mod,type=cache \
    --mount=target=/root/.cache/go-build,type=cache \
    apk add autoconf automake bash file g++ gcc git libtool linux-headers make musl-dev unbound-dev && \
    cd /root/dvpnd/ && make --jobs=$(nproc) install && \
    git clone --branch=v2.0.0 --depth=1 https://github.com/handshake-org/hnsd.git /root/hnsd && \
    git -C /root/hnsd rev-parse HEAD | grep -qx a5c7c287e848f46d3e97f16b698e2027c8dc96c3 && \
    cd /root/hnsd/ && bash autogen.sh && sh configure && make --jobs=$(nproc) && \
    git clone --quiet --branch=${AWG_GO_TAG} --depth=1 https://github.com/amnezia-vpn/amneziawg-go.git /root/amneziawg-go && \
    git -C /root/amneziawg-go rev-parse HEAD | grep -q ^${AWG_GO_COMMIT} && \
    cd /root/amneziawg-go && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /root/amneziawg-go/amneziawg-go . && \
    git clone --quiet --branch=${AWG_TOOLS_TAG} --depth=1 https://github.com/amnezia-vpn/amneziawg-tools.git /root/amneziawg-tools && \
    git -C /root/amneziawg-tools rev-parse HEAD | grep -q ^${AWG_TOOLS_COMMIT} && \
    make -C /root/amneziawg-tools/src --jobs=$(nproc) && \
    make -C /root/amneziawg-tools/src DESTDIR=/root/awg-install PREFIX=/usr WITH_WGQUICK=yes WITH_BASHCOMPLETION=no WITH_SYSTEMDUNITS=no install

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

COPY --from=build /go/bin/dvpnd /usr/local/bin/process
COPY --from=build /root/hnsd/hnsd /usr/local/bin/hnsd
COPY --from=build /tmp/xray/xray /usr/local/bin/xray
COPY --from=build /tmp/hysteria /usr/local/bin/hysteria
COPY --from=build /root/amneziawg-go/amneziawg-go /usr/bin/amneziawg-go
COPY --from=build /root/awg-install/usr/bin/awg /root/awg-install/usr/bin/awg-quick /usr/bin/

# The rest comes from the packages of this Alpine release, signed by Alpine and
# not pinned to a version: v2ray, OpenVPN, iptables, wireguard-tools.
# dvpnd-proxy is the account the node runs the proxy daemons as, and that
# OpenVPN drops to (services/common/runtime.go).
RUN apk add --no-cache iptables openvpn unbound-libs v2ray wireguard-tools && \
    rm -rf /etc/v2ray/ /usr/share/v2ray/ && \
    addgroup -S dvpnd-proxy && \
    adduser -S -D -H -G dvpnd-proxy -h /nonexistent -s /sbin/nologin dvpnd-proxy

CMD ["process"]
