#!/usr/bin/env bash
# Share several unrelated files. vrok generates an index page for them; the
# files keep their names but their directories are never exposed.
#
# Run: ./examples/share-multiple.sh
set -euo pipefail

VROK="$(dirname "$0")/../bin/vrok"
[ -x "$VROK" ] || { echo "run 'make build' first"; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"; kill ${share_pid:-0} 2>/dev/null || true' EXIT

# Three files from three different places, which is the point of this mode.
mkdir -p "$workdir/a" "$workdir/b"
printf '{\n  "ok": true,\n  "count": 3\n}\n' > "$workdir/a/results.json"
printf '# Notes\n\nWorks on my machine.\n'  > "$workdir/b/notes.md"
head -c 4096 /dev/urandom                   > "$workdir/dump.bin"

echo "== sharing three files from three directories =="
"$VROK" "$workdir/a/results.json" "$workdir/b/notes.md" "$workdir/dump.bin" \
  --ttl 5m --name "Build 418" > "$workdir/out.txt" 2>&1 &
share_pid=$!
sleep 1.5

url=$(grep 'URL:' "$workdir/out.txt" | grep -oE 'http://[^ ]+')
cat "$workdir/out.txt"

echo
echo "== the generated index lists all three =="
curl -s "$url" | grep -oE 'results\.json|notes\.md|dump\.bin' | sort -u | sed 's/^/  /'

echo
echo "== each file gets the preview its type deserves =="
# JSON is tokenised and highlighted on the server, so the page needs no script.
curl -s "${url}results.json" | grep -o 'tok-[a-z]*' | sort -u | tr '\n' ' ' \
  | sed 's/^/  results.json  highlighted spans: /;s/$/\n/'
# Markdown is rendered, with any raw HTML in the source discarded.
curl -s "${url}notes.md" | grep -o '<h1>Notes</h1>' \
  | sed 's/^/  notes.md      rendered to: /'
# A binary has no useful preview, so it is offered as a download.
curl -sI "${url}dump.bin?dl=1" | grep -i 'content-disposition' \
  | sed 's/^/  dump.bin      /'

echo
echo "== only the generated names resolve; the real directories do not exist here =="
for path in "a/results.json" "$workdir/a/results.json"; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --path-as-is "${url}${path#/}")
  printf '  %-40s -> %s\n' "${path:0:40}" "$code"
done
