# An AmneziaWG client: amneziawg-go and the tools at the given tags, the way
# a client app bundles them.
FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
ARG GO_TAG
ARG TOOLS_TAG
RUN apk add --no-cache bash gcc git linux-headers make musl-dev
RUN git clone --quiet --branch=${GO_TAG} --depth=1 https://github.com/amnezia-vpn/amneziawg-go.git /src/go && \
    cd /src/go && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/amneziawg-go . && \
    git clone --quiet --branch=${TOOLS_TAG} --depth=1 https://github.com/amnezia-vpn/amneziawg-tools.git /src/tools && \
    make -C /src/tools/src --jobs=$(nproc) && \
    make -C /src/tools/src DESTDIR=/out/tools PREFIX=/usr WITH_WGQUICK=yes WITH_BASHCOMPLETION=no WITH_SYSTEMDUNITS=no install

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache bash curl iproute2 iptables
COPY --from=build /out/amneziawg-go /usr/bin/amneziawg-go
COPY --from=build /out/tools/usr/bin/awg /out/tools/usr/bin/awg-quick /usr/bin/
COPY run-client.sh /usr/local/bin/run-client
RUN chmod 0755 /usr/local/bin/run-client
ENTRYPOINT ["/usr/local/bin/run-client"]
