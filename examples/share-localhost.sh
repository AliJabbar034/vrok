#!/usr/bin/env bash
# Share a local HTTP server. vrok reverse-proxies it, rewriting redirects and
# cookie paths so the app behaves as if it were mounted at the share URL.
#
# Run: ./examples/share-localhost.sh
set -euo pipefail

VROK="$(dirname "$0")/../bin/vrok"
[ -x "$VROK" ] || { echo "run 'make build' first"; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"; kill ${share_pid:-0} ${app_pid:-0} 2>/dev/null || true' EXIT

# A tiny app that does the things a proxy has to get right: redirect, set a
# cookie, echo a POST body, and 404.
cat > "$workdir/app.go" <<'GO'
package main

import (
	"fmt"
	"io"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
		fmt.Fprintf(w, "hello from %s\n", r.Host)
	})
	http.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusFound)
	})
	http.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "the new location")
	})
	http.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "you sent: %s\n", body)
	})
	http.ListenAndServe("127.0.0.1:3000", nil)
}
GO

echo "== starting the app on 127.0.0.1:3000 =="
(cd "$workdir" && go run app.go) &
app_pid=$!
sleep 2

echo "== sharing it =="
# ':3000', 'localhost:3000' and '3000' all mean the same thing.
"$VROK" localhost:3000 --ttl 5m > "$workdir/out.txt" 2>&1 &
share_pid=$!
sleep 1.5

url=$(grep 'URL:' "$workdir/out.txt" | grep -oE 'http://[^ ]+')
cat "$workdir/out.txt"

echo
echo "== the app answers through the share =="
curl -s "$url"

echo
echo "== a redirect is rewritten to stay inside the share =="
curl -sI "${url}old" | grep -iE '^HTTP|^location' | sed 's/^/  /'

echo
echo "== the cookie is rescoped to the share path, so it is actually sent back =="
curl -sI "$url" | grep -i 'set-cookie' | sed 's/^/  /'

echo
echo "== request bodies and methods are forwarded unchanged =="
curl -s -X POST --data 'a payload' "${url}echo" | sed 's/^/  /'

echo
echo "== the app's own 404 passes through as a 404 =="
curl -s -o /dev/null -w '  /missing -> %{http_code}\n' "${url}missing"

echo
echo "Note: --downloads is ignored for HTTP shares. Every asset request"
echo "would count against it, which would make the flag useless."
