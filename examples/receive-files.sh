#!/usr/bin/env bash
# Receive files, then prove that a sender cannot write outside the folder,
# replace a file already there, or send more than it was allowed to.
#
# The upload page is what a person uses. This drives the same API with curl:
# offer the names and sizes, then begin, send and finish each file.
#
# Run: ./examples/receive-files.sh
set -euo pipefail

VROK="$(dirname "$0")/../bin/vrok"
[ -x "$VROK" ] || { echo "run 'make build' first"; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"; kill ${share_pid:-0} 2>/dev/null || true' EXIT
inbox="$workdir/inbox"
mkdir "$inbox"
echo "already here" > "$inbox/notes.txt"
echo "secret" > "$workdir/outside.txt"

# --yes accepts every offer: there is no terminal here to press y in.
echo "== receiving into $inbox, at most 2 files =="
"$VROK" receive "$inbox" --yes --max-files 2 --tunnel local > "$workdir/out.txt" 2>&1 &
share_pid=$!
sleep 1.5

url=$(grep 'URL:' "$workdir/out.txt" | grep -oE 'http://[^ ]+')
cat "$workdir/out.txt"

api() { curl -s -H 'X-Vrok-Upload: 1' "$@"; }
field() { sed -nE "s/.*\"$1\":\"?([^\",}]*).*/\1/p"; }

# send OFFER NAME FILE: begin an upload, send it in one chunk, finish it.
send() {
  local size id
  size=$(wc -c < "$3" | tr -d ' ')
  id=$(api -X POST "${url}_upload" \
    -d "{\"offer\":\"$1\",\"name\":\"$2\",\"size\":$size}" | field id)
  api -X PUT --data-binary "@$3" "${url}_upload/$id?offset=0" > /dev/null
  api -X POST "${url}_upload/$id" > /dev/null
}

echo
echo "== a request without the upload page's header is refused =="
curl -s -o /dev/null -w '  POST _offer -> %{http_code}\n' -X POST "${url}_offer" -d '{}'

echo
echo "== a file that was never offered is refused =="
api -o /dev/null -w '  POST _upload -> %{http_code}\n' -X POST "${url}_upload" \
  -d '{"offer":"made-up","name":"sneaky.txt","size":5}'

echo
echo "== offering two files, one named to climb out of the folder =="
printf 'hello' > "$workdir/a"
printf 'world' > "$workdir/b"
offer=$(api -X POST "${url}_offer" \
  -d '{"files":[{"name":"../outside.txt","size":5},{"name":"notes.txt","size":5}]}')
echo "  $offer"
id=$(echo "$offer" | field id)

send "$id" "../outside.txt" "$workdir/a"
send "$id" "notes.txt" "$workdir/b"

echo
echo "== what arrived =="
ls -1 "$inbox" | sed 's/^/  /'

fail=0
[ "$(cat "$workdir/outside.txt")" = "secret" ] || { echo "FAIL: wrote outside the folder"; fail=1; }
[ "$(cat "$inbox/notes.txt")" = "already here" ] || { echo "FAIL: replaced an existing file"; fail=1; }
[ "$(cat "$inbox/outside.txt")" = "hello" ] || { echo "FAIL: the climbing name did not land inside"; fail=1; }
[ "$(cat "$inbox/notes (1).txt")" = "world" ] || { echo "FAIL: the duplicate was not renamed"; fail=1; }

echo
echo "== the link took its 2 files, so a third offer is refused =="
api -o /dev/null -w '  POST _offer -> %{http_code}\n' -X POST "${url}_offer" \
  -d '{"files":[{"name":"more.txt","size":5}]}'

kill $share_pid 2>/dev/null || true
sleep 0.5
echo
tail -5 "$workdir/out.txt"
[ $fail -eq 0 ] && echo && echo "OK: every file landed inside the folder, and nothing was replaced."
exit $fail
