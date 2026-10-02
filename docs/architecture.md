# Architecture

vrok is one Go module with two binaries and a set of packages that depend
inwards. The aim is that each layer can be understood, tested and replaced on
its own.

## The shape of a share

```
            vrok ./video.mp4
                   │
        ┌──────────┴───────────┐
        │  1. classify source  │   internal/sharing
        │  2. mint id + token  │   internal/security
        │  3. hash password    │
        │  4. register share   │
        └──────────┬───────────┘
                   │
        ┌──────────┴───────────┐
        │  5. bind HTTP server │   internal/server
        │  6. start tunnel     │   internal/tunnel
        │  7. print URL        │   internal/ui
        └──────────┬───────────┘
                   │
              wait for Ctrl+C, expiry or revoke
```

Steps 5 and 6 are in that order on purpose: the listener is bound before any
URL is printed and before a tunnel is opened, so a port conflict fails before
anyone is handed a link that cannot work.

## Request path

```
GET https://a82kd9.vrok.example.com/s/8kLmP3qR7wXz2vN4bYtJcA/screenshots/one.png
                              │
                    ┌─────────▼─────────┐
                    │  relay (optional) │  internal/relay
                    │  host → agent     │
                    └─────────┬─────────┘
                              │ WebSocket, HTTP semantics intact
                    ┌─────────▼─────────┐
                    │  dispatcher       │  internal/server/router.go
                    │                   │
                    │  token → share    │  registry lookup
                    │  guards           │  expiry, revoke, download limit
                    │  password gate    │  Argon2id + signed cookie
                    │  method check     │
                    └─────────┬─────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
   SingleFileHandler   DirectoryHandler     ProxyHandler
   FileSetHandler      + PathResolver       → localhost:3000
          │                   │
          └─────────┬─────────┘
                    ▼
            http.ServeContent          Range, ETag, 206, HEAD, 304
```

Everything upstream of the handlers is shared, so expiry and password rules
cannot be forgotten by a new share type. Everything downstream is specific to
one kind of share, selected from a map keyed by `sharing.Kind`.

## Packages

| Package             | Responsibility                                     | Depends on                                 |
| ------------------- | -------------------------------------------------- | ------------------------------------------ |
| `internal/sharing`  | The domain: what a share is, when it stops working | nothing                                    |
| `internal/security` | Tokens, Argon2id, HMAC, path confinement           | nothing                                    |
| `internal/preview`  | Which renderer presents which file                 | `humanize`                                 |
| `internal/server`   | HTTP routing, file serving, reverse proxy          | `sharing`, `security`, `preview`, `viewer` |
| `internal/tunnel`   | Publishing a local server publicly                 | `protocol`                                 |
| `internal/protocol` | The CLI to relay wire format                       | nothing                                    |
| `internal/relay`    | The relay server                                   | `protocol`, `security`                     |
| `internal/control`  | One process inspecting another's shares            | nothing                                    |
| `internal/config`   | Stored defaults                                    | nothing                                    |
| `internal/ui`       | Terminal output                                    | `humanize`                                 |
| `internal/cli`      | Command wiring                                     | everything                                 |
| `web/viewer`        | Embedded HTML and CSS                              | nothing                                    |

`internal/cli` is the only package that depends on all the others. It is the
composition root: it builds the object graph and nothing else builds it.

## Dependency inversion in practice

The domain does not import the crypto package. `sharing.Factory` needs random
tokens and a password hasher, so it declares what it needs:

```go
// internal/sharing/factory.go
type TokenSource interface {
    NewID() (string, error)
    NewToken() (string, error)
}

type PasswordHasher interface {
    Hash(password string) (string, error)
}
```

`internal/security` happens to satisfy both, and `internal/cli` is what puts
them together. The domain can be tested with a counter and a string
concatenation, which is exactly what its tests use.

The HTTP layer does the same thing. `server.Options` takes interfaces for the
resolver, the guards, the hasher, the signer, the clock, the detector and the
logger. The server test suite runs against a cheap hasher and a one-share
registry, with no tunnel and no terminal in sight.

## Interfaces that exist to be extended

**`tunnel.Tunnel`** — the most important one. Providers register themselves:

```go
func init() { Register("cloudflare", newCloudflare) }
```

Adding Fly.io or a custom relay means adding one file. No switch statement
anywhere changes, and the CLI keeps printing whatever URL it is handed.

Cloudflare is not a second implementation; it is a `processSpec` driving one
`processTunnel` that runs a child process and scrapes its output for a URL.
A new binary-backed provider is another spec, not another type.

**`preview.Renderer`** — one type per media category, chosen by the first one
that reports `Supports`. `Registry.With` prepends renderers, so previews can be
extended from outside the package. A renderer that fails degrades to the
download card rather than a 500: the visitor wants the bytes, not the preview.

**`sharing.Guard`** — one rule per type, composed in a slice. An IP allowlist or
an access window would be a new `Guard`, not a change to the handlers that
enforce them.

**`server.ShareHandler`** — one handler per `sharing.Kind`, held in a map.

**`control.Provider`** — how a sharing process exposes itself to `vrok list`.

## State

There is no database, no Redis and no file of shares. The registry is a map
behind an `RWMutex`:

```go
type Registry struct {
    mu      sync.RWMutex
    byID    map[string]*Share
    byToken map[string]*Share
}
```

This is not a shortcut. "The share disappears when the process stops" is the
product, and in-memory state is the most direct way to guarantee it. There is
no reconciliation to get wrong, nothing to clean up after a crash, and no way
for a share to outlive the process that promised to serve it.

Two indexes exist because the two lookups are different: visitors present a
token, and the owner presents a short id. Keeping them separate means the id can
be short enough to read aloud without weakening the token.

The one piece of mutable per-share state that needs care is the download
counter. `ClaimDownload` reads and increments under a single lock before the
file is opened, so `--downloads 5` is a hard limit even when five requests
arrive simultaneously. If the transfer then delivers nothing — a 304, or a
client that disappeared — `ReleaseDownload` puts the allowance back.

## Cross-process management

`vrok list` has to show shares that live in another process's memory. Each
sharing process serves a small JSON API on a Unix socket:

```
$XDG_STATE_HOME/vrok/sessions/<pid>.sock

GET  /shares       → []ShareInfo
POST /revoke/{id}  → {"revoked": bool}
POST /stop         → {"stopping": true}
```

`vrok list` scans the directory, dials each socket and merges the answers. A
socket left behind by a killed process fails to dial and is deleted. The
directory is `0700` and each socket is `0600`, because reaching one is enough to
stop someone's shares.

If the socket cannot be created, sharing still works; only the management
commands lose sight of it, and the CLI says so.

## Timeouts

`ReadHeaderTimeout` is set and `WriteTimeout` deliberately is not. A write
deadline applies to the whole response, so any value large enough for a
multi-gigabyte download over a slow link is too large to be a protection, and
any value small enough to protect would cut off legitimate transfers. Slow
clients are bounded by the read header timeout and the idle timeout instead.

## Known limits

- The relay forwards request/response pairs, not arbitrary bidirectional
  streams, so a WebSocket upgrade through `--tunnel relay` returns 501. HMR over
  a Cloudflare tunnel works, because that provider proxies upgrades itself.
- One tunnel is one WebSocket connection. Body frames are buffered per stream,
  but a sufficiently slow visitor can still slow down others sharing the
  connection. Separate QUIC streams are the real fix.
- `--downloads` is ignored for HTTP shares, where every asset request would
  count against it. The CLI says so rather than silently misbehaving.
