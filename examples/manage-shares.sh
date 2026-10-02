#!/usr/bin/env bash
# List, revoke and stop shares that are running in other processes.
#
# There is no database. Each sharing process serves a small API on a Unix
# socket under $XDG_STATE_HOME/vrok/sessions, and these commands scan them.
#
# Run: ./examples/manage-shares.sh
set -euo pipefail

cd "$(dirname "$0")/.."
VROK=./bin/vrok
[ -x "$VROK" ] || { echo "run 'make build' first"; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"; kill ${a_pid:-0} ${b_pid:-0} 2>/dev/null || true' EXIT

echo 'first'  > "$workdir/alpha.txt"
echo 'second' > "$workdir/beta.txt"

echo "== starting two shares, in two separate processes =="
$VROK "$workdir/alpha.txt" --ttl 10m            > "$workdir/a.log" 2>&1 & a_pid=$!
$VROK "$workdir/beta.txt"  --ttl 1h --downloads 5 > "$workdir/b.log" 2>&1 & b_pid=$!
sleep 2

url_a=$(grep 'URL:' "$workdir/a.log" | grep -oE 'http://[^ ]+')
echo "  pid $a_pid  alpha.txt"
echo "  pid $b_pid  beta.txt"

echo
echo "== vrok list sees both, across process boundaries =="
$VROK list

echo
echo "== and in JSON, for scripting =="
$VROK list --json

echo
echo "== alpha is reachable right now =="
curl -s -o /dev/null -w '  %{http_code}\n' --noproxy '*' "$url_a"

id_a=$($VROK list --json | sed -n 's/.*"id": *"\([^"]*\)".*"name": *"alpha.txt".*/\1/p')
[ -n "$id_a" ] || id_a=$($VROK list --json \
  | tr '}' '\n' | grep alpha.txt | grep -oE '"id": *"[^"]*"' | head -1 | cut -d'"' -f4)

echo
echo "== revoking it by id =="
$VROK revoke "$id_a"

sleep 1

echo
echo "== the URL is gone =="
# A process with nothing left to serve exits, so this is a refused connection
# rather than a 404 — which is as temporary as access gets.
code=$(curl -s -o /dev/null -w '%{http_code}' --noproxy '*' --max-time 3 "$url_a" || true)
case "$code" in
  000) echo "  connection refused: the process had no shares left and exited" ;;
  404) echo "  404: the process is still serving other shares" ;;
  *)   echo "  unexpected: $code" ;;
esac

echo
echo "== one share left =="
$VROK list

echo
echo "== stop --all shuts down every sharing process =="
$VROK stop --all
sleep 1

echo
echo "== nothing is running, and the stale sockets have been cleared =="
$VROK list || true

echo
echo "No state survives this: the shares lived in those two processes'"
echo "memory, and the sockets under \$XDG_STATE_HOME only pointed at them."
