#!/usr/bin/env bash
# Share a directory. A folder with an index.html is served like the real site,
# the listing is still reachable, and nothing outside the folder is.
#
# Run: ./examples/share-directory.sh
set -euo pipefail

VROK="$(dirname "$0")/../bin/vrok"
[ -x "$VROK" ] || { echo "run 'make build' first"; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"; kill ${share_pid:-0} 2>/dev/null || true' EXIT

# A plausible test report, plus a secret next to it that must stay unreachable.
mkdir -p "$workdir/report/assets"
cat > "$workdir/report/index.html" <<'HTML'
<!doctype html>
<title>Test report</title>
<link rel="stylesheet" href="assets/report.css">
<h1>42 passed, 0 failed</h1>
HTML
echo 'h1 { font-family: system-ui }' > "$workdir/report/assets/report.css"
echo 'notes' > "$workdir/report/notes.txt"
echo 'DO NOT SHARE' > "$workdir/secret.txt"

echo "== sharing $workdir/report =="
"$VROK" "$workdir/report" --ttl 5m > "$workdir/out.txt" 2>&1 &
share_pid=$!
sleep 1.5

url=$(grep 'URL:' "$workdir/out.txt" | grep -oE 'http://[^ ]+')
cat "$workdir/out.txt"

echo
echo "== the root serves index.html, not a file listing =="
curl -s "$url" | head -3

echo
echo "== its own assets load, so the report works =="
curl -s -o /dev/null -w '  assets/report.css -> %{http_code} %{content_type}\n' \
  "${url}assets/report.css?raw=1"

echo
echo "== ?list=1 forces the browsable listing =="
curl -s "${url}?list=1" | grep -oE 'notes\.txt|assets' | sort -u | sed 's/^/  /'

echo
echo "== nothing outside the shared folder is reachable =="
# -L because an unencoded ../ is normalised by the HTTP layer before vrok
# sees it; following the redirect is what a browser does, and what matters is
# where it lands.
for path in "../secret.txt" "..%2Fsecret.txt" "%2e%2e/secret.txt" "..%2f..%2fetc%2fpasswd" "/etc/passwd"; do
  body=$(curl -s -L --path-as-is -w '\n%{http_code}' "${url}${path}")
  code=$(echo "$body" | tail -1)
  printf '  %-26s -> %s' "$path" "$code"
  case "$body" in
    *"DO NOT SHARE"*|*"root:"*) echo "  *** LEAKED ***" ;;
    *) echo "" ;;
  esac
done

echo
echo "404 rather than 403 is deliberate: a 403 would confirm the file exists."
echo "Encoded traversal is rejected by vrok's path resolver, which also"
echo "resolves symlinks and re-checks that the result is still inside the root."
