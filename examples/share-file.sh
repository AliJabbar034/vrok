#!/usr/bin/env bash
# Share one file, then prove that Range requests and the download limit work.
#
# Run: ./examples/share-file.sh
set -euo pipefail

VROK="$(dirname "$0")/../bin/vrok"
[ -x "$VROK" ] || { echo "run 'make build' first"; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"; kill ${share_pid:-0} 2>/dev/null || true' EXIT

# A file big enough that partial fetches are meaningful.
head -c 2097152 /dev/urandom > "$workdir/video.mp4"

echo "== starting a share with a 5 minute TTL and a limit of 3 downloads =="
"$VROK" "$workdir/video.mp4" --ttl 5m --downloads 3 > "$workdir/out.txt" 2>&1 &
share_pid=$!
sleep 1.5

url=$(grep 'URL:' "$workdir/out.txt" | grep -oE 'http://[^ ]+')
cat "$workdir/out.txt"

echo
echo "== the preview page =="
curl -s "$url" | grep -oE '<title>[^<]*</title>'

echo
echo "== metadata, without transferring the file =="
curl -sI "${url}?raw=1" | grep -iE 'content-length|accept-ranges|etag'

echo
echo "== one 100-byte slice from the middle, which is what video seeking does =="
curl -s -D- -o /dev/null -H 'Range: bytes=1048576-1048675' "${url}?raw=1" \
  | grep -iE '^HTTP|content-range'

echo
echo "== three downloads are allowed, the fourth is not =="
for i in 1 2 3 4; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "${url}?dl=1")
  echo "  download $i -> $code"
done

echo
echo "The URL is now dead, and the process is about to report its totals."
kill $share_pid 2>/dev/null || true
sleep 0.5
tail -4 "$workdir/out.txt"
