#!/usr/bin/env bash
# Run a relay and an agent on one machine, and fetch a file through the tunnel.
#
# In production, *.your-domain points at the relay and a proxy terminates TLS.
# Here there is no wildcard DNS, so curl is told to resolve the share hostname
# to the relay's address — which is exactly what a wildcard record does.
#
# Run: ./examples/run-relay.sh
set -euo pipefail

cd "$(dirname "$0")/.."
[ -x bin/vrok ] && [ -x bin/vrok-relay ] || { echo "run 'make build' first"; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"; kill ${relay_pid:-0} ${share_pid:-0} 2>/dev/null || true' EXIT

DOMAIN=vrok.test
PORT=8787

# Everything here is loopback. A proxy in the environment would both ignore
# --resolve and answer for a hostname it cannot reach.
CURL=(curl -s --noproxy '*')

head -c 1048576 /dev/urandom > "$workdir/payload.bin"

echo "== starting the relay for *.$DOMAIN on :$PORT =="
# -scheme http because nothing is terminating TLS in this example. In
# production it stays https, since that is what visitors use.
./bin/vrok-relay -domain "$DOMAIN" -addr ":$PORT" -scheme http -verbose \
  > "$workdir/relay.log" 2>&1 &
relay_pid=$!
sleep 1

"${CURL[@]}" "http://127.0.0.1:$PORT/healthz" | sed 's/^/  healthz: /'

echo
echo "== starting an agent that connects to it =="
./bin/vrok "$workdir/payload.bin" \
  --tunnel relay --relay-url "http://127.0.0.1:$PORT" --ttl 5m \
  > "$workdir/share.log" 2>&1 &
share_pid=$!
sleep 2

cat "$workdir/share.log"
url=$(grep 'URL:' "$workdir/share.log" | grep -oE 'http://[^ ]+')
host=$(echo "$url" | sed -E 's|https?://([^/]+).*|\1|')

# The relay hands out a URL on the default port, because in production it sits
# behind a proxy on 443. Here it is on 8787, so the port goes back in and
# --resolve maps the hostname to loopback. The relay strips the port from the
# Host header either way.
fetch="${url/$host/$host:$PORT}"

echo
"${CURL[@]}" "http://127.0.0.1:$PORT/healthz" | sed 's/^/  healthz: /'

echo
echo "== fetching through the relay (--resolve stands in for wildcard DNS) =="
"${CURL[@]}" --resolve "$host:$PORT:127.0.0.1" \
  -o "$workdir/received.bin" "${fetch}?raw=1"

if cmp -s "$workdir/payload.bin" "$workdir/received.bin"; then
  echo "  1 MiB arrived byte for byte, read off disk on demand"
else
  echo "  MISMATCH"
fi

echo
echo "== Range survives the tunnel, so video seeking and resume work =="
"${CURL[@]}" --resolve "$host:$PORT:127.0.0.1" -D- -o /dev/null \
  -H 'Range: bytes=500000-500099' "${fetch}?raw=1" \
  | grep -iE '^HTTP|content-range' | sed 's/^/  /'

echo
echo "== an unknown hostname is a 404, the same as a stopped share =="
"${CURL[@]}" -o /dev/null -w "  nope.$DOMAIN -> %{http_code}\n" \
  --resolve "nope.$DOMAIN:$PORT:127.0.0.1" "http://nope.$DOMAIN:$PORT/"

echo
echo "== stopping the agent takes the public URL with it =="
kill $share_pid 2>/dev/null || true
sleep 1
"${CURL[@]}" -o /dev/null -w '  the share URL now answers %{http_code}\n' \
  --resolve "$host:$PORT:127.0.0.1" "${fetch}?raw=1"
"${CURL[@]}" "http://127.0.0.1:$PORT/healthz" | sed 's/^/  healthz: /'

echo
echo "The relay never held the file. It routed a request to the machine the"
echo "file was on and streamed the answer back."
