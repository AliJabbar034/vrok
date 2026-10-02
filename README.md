<p align="center">
  <img src="docs/logo.svg" alt="vrok" width="96" height="96" />
</p>

# vrok

Share local files, folders and development servers through temporary URLs.

```console
$ vrok ./video.mp4
✓ Sharing video.mp4

  URL:         https://avi-downtown-justin-postings.trycloudflare.com/s/8kLmP3qR7wXz2vN4bYtJcA/
  Reachable:   anyone with the link
  Expires:     2h
  Tunnel:      cloudflare

  Press Ctrl+C to stop
```

No account, no configuration, no separate install. The URL works from anywhere
and can be sent to anyone.

Nothing is uploaded. Nothing is stored anywhere. Requests are served from your
machine while the command runs, and the URL stops working the moment the share
expires, hits its download limit, or you press Ctrl+C.

Every banner says who can open the URL, so you always know what you are about
to paste into a chat. To keep a share off the internet, see
[Staying private](#staying-private).

## Install

Every option below is also laid out at
[alijabbar034.github.io/vrok](https://alijabbar034.github.io/vrok/), which
resolves the download links against the newest release.

**macOS and Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/AliJabbar034/vrok/main/install.sh | sh
```

**Windows**

```powershell
irm https://raw.githubusercontent.com/AliJabbar034/vrok/main/install.ps1 | iex
```

Both scripts detect your platform, verify the download against `checksums.txt`,
and install without administrator rights.

**Package managers**

```sh
brew install --cask AliJabbar034/tap/vrok  # macOS, Linux

scoop bucket add vrok https://github.com/AliJabbar034/scoop-bucket
scoop install vrok                         # Windows
winget install AliJabbar034.vrok           # Windows

sudo apt install ./vrok_*_amd64.deb        # Debian, Ubuntu
sudo dnf install ./vrok_*_amd64.rpm        # Fedora, RHEL
sudo apk add --allow-untrusted ./vrok_*.apk  # Alpine
```

**From source**

```sh
go install github.com/AliJabbar034/vrok/cmd/vrok@latest

# or from a clone
make build        # ./bin/vrok and ./bin/vrok-relay
make install      # into GOPATH/bin
```

**Manually**

Download an archive for your platform from
[Releases](https://github.com/AliJabbar034/vrok/releases), then put `vrok` somewhere on
your `PATH`. Every release ships builds for:

|         | amd64 | arm64 | armv7 |
| ------- | :---: | :---: | :---: |
| macOS   |   ✓   |   ✓   |       |
| Linux   |   ✓   |   ✓   |   ✓   |
| Windows |   ✓   |   ✓   |       |
| FreeBSD |   ✓   |       |       |

Tab completion is included in the archive and installed automatically by
Homebrew and the Linux packages. To set it up by hand:

```sh
vrok completion zsh  > "${fpath[1]}/_vrok"             # zsh
vrok completion bash > /etc/bash_completion.d/vrok     # bash
vrok completion fish > ~/.config/fish/completions/vrok.fish
```

## Use

```sh
vrok ./demo.mp4                  # one file, with a video player and seeking
vrok ./playwright-report         # a whole directory, browsable
vrok report.pdf shot.png a.mp4   # several files behind one index page
vrok localhost:3000              # a local HTTP server, reverse-proxied
```

`vrok share ./x` and `vrok ./x` are the same command.

### Options

| Flag                   | Meaning                                                                                               |
| ---------------------- | ----------------------------------------------------------------------------------------------------- |
| `--ttl 30m`            | How long the share lives. Accepts `45s`, `30m`, `2h`, `1d`, `1w`, or `0` for no expiry. Default `2h`. |
| `--downloads 5`        | Stop sharing after five downloads.                                                                    |
| `--password`           | Ask for a password that visitors must enter. Also reads `VROK_PASSWORD`.                              |
| `--qr`                 | Print a QR code for the URL.                                                                          |
| `--local`              | Serve on the local network only, with no public tunnel.                                               |
| `--tunnel <name>`      | `auto` (default), `local`, `cloudflare` or `relay`.                                                   |
| `--name client-report` | Display name for the share.                                                                           |
| `--port 8080`          | Pick the local port instead of a free one.                                                            |
| `-v`                   | Show request and tunnel diagnostics.                                                                  |

### Managing shares

```sh
vrok list               # every share running on this machine
vrok list --json        # the same, for scripts
vrok revoke a82kd9      # make one URL stop working now
vrok stop --all         # stop every vrok process
vrok config set ttl 30m # store a default
```

`vrok list` has no database behind it. Each sharing process reports its own
shares over a socket in your state directory, which is why a share disappears
from the list the instant its process exits.

## What visitors see

Any file can be shared, whatever its type or size — a 4 GB disk image, an
`.xlsx`, an unsigned installer, a file with no extension. The table below only
decides how a file is _presented_: recognised types get a preview, and
everything else gets a download card. There is no allowed-type list.

| Type                            | Preview                                    |
| ------------------------------- | ------------------------------------------ |
| JPG, PNG, WebP, GIF, SVG        | Image                                      |
| MP4, WebM, MOV, MKV             | Video player with seeking                  |
| MP3, WAV, FLAC, OGG             | Audio player                               |
| PDF                             | The browser's PDF viewer                   |
| Markdown                        | Rendered, with tables and task lists       |
| JSON                            | Pretty-printed and syntax highlighted      |
| TXT, logs, source code          | Plain text                                 |
| HTML                            | Rendered                                   |
| ZIP, JAR                        | File listing, without extracting           |
| Makefile, LICENSE, CHANGELOG    | Plain text, despite having no extension    |
| XLSX, DOCX, PPTX, DMG, EXE, APK | Download card, with the right Content-Type |
| Anything else                   | Download card                              |

Range requests are answered end to end, including through a tunnel, so video
seeking works and an interrupted download of a large file resumes rather than
restarting.

A directory containing `index.html` serves that file at its root, which is what
makes `vrok ./playwright-report` or `vrok ./dist` behave like the real thing.
Add `?list=1` to any directory URL to see the file listing instead.

## How the public URL happens

Your machine has no public IP; it sits behind NAT, so nothing on the internet
can connect _to_ it. A tunnel is the way around that: a host that does have a
public IP accepts the visitor's request and forwards it down a connection vrok
opened outward.

By default vrok picks that route for you, in this order:

1. **A relay you configured**, if there is one. Yours beats anyone else's.
2. **`cloudflared` already on PATH**, if it is installed.
3. **Otherwise it fetches `cloudflared`** into its cache (about 40 MB, once)
   and opens a free Cloudflare quick tunnel. No Cloudflare account involved.

Step 3 is what makes `vrok ./demo.pdf` work on a machine with nothing set up.
It is announced while it happens, cached per vrok version, and only ever
reached when steps 1 and 2 come up empty.

If no route can be opened at all, vrok fails and says so. It will not quietly
hand you a loopback link after you watched it try to go public.

```sh
vrok ./video.mp4                           # auto: a public https URL
vrok ./video.mp4 --tunnel cloudflare       # force one provider
vrok ./video.mp4 --tunnel relay \
  --relay-url https://relay.example.com    # a relay you run yourself
```

Every provider sits behind one interface, so none of them is special and adding
another is a single file. See [docs/architecture.md](docs/architecture.md).

## Staying private

A public URL is the default because sharing is the point, but it is one flag
away from not being one:

```sh
vrok ./video.mp4 --local          # your network only: http://192.168.1.20:8080/...
vrok ./video.mp4 --tunnel local   # this machine only: http://127.0.0.1:51182/...
```

A `127.0.0.1` URL cannot be sent to anyone — on their machine it means _their_
computer, not yours — which is why the banner labels it `this machine only`.

To make a private default permanent:

```sh
vrok config set tunnel local
```

### Running your own relay

The default route borrows Cloudflare's infrastructure, which means your share
gets a `trycloudflare.com` hostname and passes through their network. If you
would rather own the whole path, vrok ships the server half too.

The relay is a router, not a store: it forwards requests into the WebSocket
tunnel held open by your CLI and streams the response straight back. It never
writes a file.

Deploy it once on a host with a public IP and your own domain:

```sh
# 1. a wildcard DNS A record: *.vrok.example.com -> this host
# 2. a wildcard certificate (DNS-01, e.g. certbot with your DNS provider)
# 3. run it
./bin/vrok-relay -domain vrok.example.com -addr :443 \
  -tls-cert /etc/vrok/fullchain.pem \
  -tls-key  /etc/vrok/privkey.pem
```

The relay terminates TLS itself when given a certificate, so there is no nginx
or Caddy in the picture. Omit `-tls-cert`/`-tls-key` if a load balancer already
terminates TLS. Set `VROK_RELAY_TOKEN` to require a credential, and pass the
same value to the CLI with `--relay-token`, if the relay should not be open to
anyone.

Then point the CLI at it once, and public https URLs become the default with
no flags at all:

```console
$ vrok config set tunnel relay
$ vrok config set relay-url https://vrok.example.com

$ vrok ./demo.pdf
  URL:         https://6acvztrd.vrok.example.com/s/l5S0OXjQDYpB7Cq9JIDveQ/
  Reachable:   anyone with the link
```

[examples/run-relay.sh](examples/run-relay.sh) runs a relay and an agent on one
machine and fetches a file through the tunnel.
See [docs/protocol.md](docs/protocol.md) for the wire format.

## Security

- URLs carry 128 bits of randomness. There are no sequential ids and nothing to
  enumerate.
- A visitor never supplies a filesystem path. The token resolves to a share,
  and the share decides which bytes exist.
- Directory shares are confined both lexically and after resolving symlinks, so
  a link inside a shared folder cannot point out of it.
- Passwords are stored as Argon2id digests and never written to disk.
- Expired, revoked, used-up and non-existent shares all answer identically, so
  a URL cannot be probed for which of those it is.

[docs/security.md](docs/security.md) describes the model and its limits.

## Documentation

- [Architecture](docs/architecture.md) — the layers and why they are separate
- [Protocol](docs/protocol.md) — the CLI to relay wire format
- [Security](docs/security.md) — threat model, guarantees and non-guarantees
- [Development](docs/development.md) — building, testing and extending
- [Examples](examples/) — runnable scripts for each kind of share

## Contributing

Branch off `main`, name the branch after the work, and open a pull request
whose title is a conventional commit. The version number and the release are
computed from that title — nobody tags by hand.

```sh
git switch -c fix/range-requests
make check
git commit -m "fix(server): honour If-Range on resumed downloads"
```

[CONTRIBUTING.md](CONTRIBUTING.md) has the full process: branch naming, commit
types and the version each one produces, what happens when a pull request
merges, and how releases are built and verified.

Security issues go through [SECURITY.md](SECURITY.md), not the issue tracker.

## License

MIT. See [LICENSE](LICENSE).
