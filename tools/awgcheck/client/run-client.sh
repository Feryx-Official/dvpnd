#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# run-client <conf> <gateway>: bring the tunnel up from the node's payload,
# check the handshake, ping the node's tunnel address and fetch over HTTPS
# through it, then show the counters and take the tunnel down.
set -u
conf="$1"; gw="$2"
echo "== engine: $(awg --version 2>&1 | head -1)"
mkdir -p /etc/amnezia/amneziawg && cp "$conf" /etc/amnezia/amneziawg/awgc.conf
if ! awg-quick up awgc; then echo "RESULT: awg-quick up FAILED"; exit 1; fi
sleep 4
awg show awgc | grep -i "handshake\|transfer" || true
if ping -c 3 -W 2 "$gw" >/dev/null 2>&1; then echo "ping $gw: ok"; else echo "ping $gw: FAILED"; fi
curl -sS --max-time 20 -o /dev/null -w "https 1.1.1.1 via tunnel: HTTP %{http_code}, %{size_download} bytes\n" https://1.1.1.1/ || echo "https via tunnel: FAILED"
curl -sS --max-time 20 -o /dev/null -w "download via tunnel: %{size_download} bytes of https://1.1.1.1/cdn-cgi/trace\n" https://1.1.1.1/cdn-cgi/trace || echo "download via tunnel: FAILED"
echo "== counters"; awg show awgc transfer
if awg show awgc latest-handshakes | awk '{exit !($2 > 0)}'; then echo "RESULT: handshake ok"; else echo "RESULT: NO HANDSHAKE"; fi
awg-quick down awgc
