# Development

## Requirements

Go 1.27 or newer. Nothing else: templates and CSS are embedded in the binary,
so there is no asset pipeline and no build step beyond `go build`.

## Common tasks

```sh
make build      # ./bin/vrok and ./bin/vrok-relay
make test       # go test ./...
make race       # go test -race ./...
make cover      # coverage summary, and coverage.html
make check      # fmt check, vet, build, test — what CI runs
make run ARGS="./README.md --ttl 5m"
make clean
```

## Layout

```
cmd/vrok/            the CLI
cmd/vrok-relay/      the relay daemon
internal/            one package per concern; see docs/architecture.md
web/viewer/          embedded templates and CSS
tests/               cross-layer integration tests
examples/            runnable scenarios
```

## Testing

Unit tests live beside their package; `tests/` holds the integration suite that
assembles real components — a registry, the HTTP server, a live relay with an
agent attached over a real WebSocket.

```sh
go test ./...                              # everything
go test ./internal/server/ -run Traversal  # one area
go test -race ./...                        # before any change to concurrency
go test ./tests/ -v                        # the assembled stack
```

Three tests in `internal/control` need `AF_UNIX` and skip themselves when the
environment forbids it, which some sandboxes do.

### What the tests are for

They encode the properties vrok promises, so changes that break a promise fail
loudly:

| Test                                                  | Property                                                    |
| ----------------------------------------------------- | ----------------------------------------------------------- |
| `internal/security` path tests                        | No URL reaches outside a share, including via symlinks      |
| `TestClaimDownloadIsAHardLimitUnderConcurrency`       | `--downloads` holds under simultaneous requests             |
| `TestRangedMediaRequestsDoNotExhaustTheDownloadLimit` | Video seeking does not spend the allowance                  |
| `TestRangeTricksCannotBypassTheDownloadLimit`         | No `Range` spelling makes a new download free               |
| `TestDownloadLimitedShareOutlivesItsLastTransfer`     | The last permitted download is not cut off                  |
| `TestUnavailableSharesReportTheSameWay`               | Expired, revoked, used-up and unknown are indistinguishable |
| `TestPasswordGate`                                    | Content is withheld, and the cookie carries no password     |
| `TestSafeNameKeepsUploadsInsideAndVisible`            | No sent name lands outside the receive folder, or hidden    |
| `TestSymlinkInInboxCannotRedirectAWrite`              | A symlink in the receive folder cannot redirect a write     |
| `TestExistingFilesAreNeverOverwritten`                | A received file never replaces one already there            |
| `TestReceiveRefusesFilesNobodyAccepted`               | Nothing is written until the owner accepts it               |
| `TestReceiveRequiresTheUploadHeader`                  | A cross-site form cannot upload                             |
| `TestPreviewsEscapeFileContent`                       | A shared file cannot inject markup                          |
| `TestRelayCarriesAShareEndToEnd`                      | Bytes and `Range` survive the tunnel                        |
| `TestShareDisappearsWhenRevoked`                      | Revoking is the same thing as the URL ceasing to exist      |

Prefer a test that states a property over one that pins an implementation
detail. Table-driven tests with one `t.Run` per case are the house style.

## Adding a tunnel provider

One file in `internal/tunnel`. If the provider is an external binary, describe
it; `processTunnel` does the rest.

```go
package tunnel

func init() { Register("myprovider", newMyProvider) }

func newMyProvider(cfg Config) (Tunnel, error) {
    return newProcessTunnel(processSpec{
        provider: "myprovider",
        binary:   "myprovider-cli",
        hint:     "Install it with `brew install myprovider`.",
        args: func(target *url.URL) []string {
            return []string{"tunnel", "--port", target.Port()}
        },
        urlPatterns: []*regexp.Regexp{
            regexp.MustCompile(`https://[a-z0-9-]+\.myprovider\.io`),
        },
    }, cfg), nil
}
```

Keep the URL pattern specific. A pattern like `https://\S+` will happily match
a documentation link in the provider's own banner. `TestFirstRangeStartParsing`
checks for exactly that mistake.

For something that is not a child process, implement `Tunnel` directly, as
`RelayTunnel` does.

## Adding a preview type

One type in `internal/preview`, registered in `NewRegistry`:

```go
type CSVRenderer struct{}

func (CSVRenderer) Supports(a Asset) bool {
    return strings.HasSuffix(a.Name, ".csv") && a.Size <= maxInlineBytes
}

func (CSVRenderer) Render(a Asset) (template.HTML, error) { … }
```

Two rules. Escape everything that came from the file — it is untrusted input.
And respect `maxInlineBytes`: a renderer must never read an unbounded amount
into memory. Returning an error is safe; the registry falls back to the
download card.

## Adding an availability rule

A `sharing.Guard`, added to `DefaultGuards`:

```go
func DuringBusinessHours() Guard {
    return GuardFunc(func(s Snapshot, now time.Time) error {
        if now.Hour() < 9 || now.Hour() >= 17 {
            return ErrExpired
        }
        return nil
    })
}
```

Guards run before any content handler, so the rule applies to every share kind
without touching a handler.

## Adding a share kind

1. A `sharing.Kind` constant and its `String()` case.
2. Recognition in `sharing.Classify`.
3. A `server.ShareHandler` implementation, added to the map in `server.New`.

The dispatcher's token lookup, guards, password gate and method check already
apply, so a new kind cannot accidentally skip them.

## Running a relay locally

```sh
go run ./cmd/vrok-relay -domain vrok.test -addr :8787 -scheme http -verbose
```

`*.vrok.test` has to resolve to your machine. Either add entries to
`/etc/hosts` for the specific share ids you use, or run dnsmasq with a wildcard.
Then:

```sh
go run ./cmd/vrok ./README.md --tunnel relay --relay-url http://localhost:8787
```

The integration test avoids the DNS problem entirely by using a client whose
dialer sends every hostname to the relay's real address, which is also what a
wildcard record does in production. See `relayClient` in `tests/e2e_test.go`.

## Release

Push a tag. `.github/workflows/release.yml` re-runs the tests, checks every
released platform still compiles, then hands over to GoReleaser:

```sh
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

That produces, from one Linux runner:

- `tar.gz` and `zip` archives for eight platforms, plus `checksums.txt`
- `.deb`, `.rpm`, `.apk` and Arch packages
- a Homebrew cask, a Scoop manifest, and a winget pull request
- a multi-platform `ghcr.io/alijabbar034/vrok-relay` image
- shell completions, generated by running the binary so they cannot drift

Then it installs the release through `install.sh` and `install.ps1` on Ubuntu,
both macOS architectures and Windows, and shares a file end to end on each.
A release that builds but cannot be installed is still a broken release.

To check the pipeline without publishing anything:

```sh
goreleaser check                                   # validate the config
goreleaser release --snapshot --clean --skip=publish
```

`make dist` does the same cross-platform archive build without GoReleaser, and
produces archives of the same shape, so `install.sh` can be tested against a
local build:

```sh
make dist VERSION=v0.2.0
(cd dist && python3 -m http.server 9000) &
VROK_VERSION=v0.2.0 \
VROK_DOWNLOAD_BASE=http://127.0.0.1:9000 \
VROK_INSTALL_DIR=/tmp/vrok-test \
  sh ./install.sh
```

`VROK_DOWNLOAD_BASE` also exists for real use: a mirror, or an air-gapped
network with the archives on an internal file server.

### Which platforms get what

The CLI ships everywhere. `vrok-relay` ships as its own archive and as a
container image, and is deliberately left out of the Homebrew cask and the
Linux packages: it is a server that needs a wildcard DNS record, not something
that belongs on a laptop's `PATH`.

### Platform-specific code

Seven files, all small, all named for what they cover:

| File                                   | Why                                               |
| -------------------------------------- | ------------------------------------------------- |
| `internal/tunnel/terminate_unix.go`    | SIGTERM lets cloudflared close its tunnel cleanly |
| `internal/tunnel/terminate_windows.go` | Windows rejects every signal except Kill          |
| `internal/control/statedir_unix.go`    | `~/.local/state`, per the XDG spec                |
| `internal/control/statedir_windows.go` | `%LOCALAPPDATA%`, which is not roamed             |
| `internal/inbox/freespace_unix.go`     | Free disk space from `statfs`                     |
| `internal/inbox/freespace_windows.go`  | Free disk space from `GetDiskFreeSpaceEx`         |
| `internal/inbox/freespace_other.go`    | Unknown elsewhere, so the check is skipped        |

`make cross` vets every released platform, and it runs before every release.
Compiling is not the same as working, though: the Windows legs of the release
workflow install and share a file for real.

## House style

- Interfaces are declared by the consumer, not the provider. `sharing` defines
  `TokenSource` because `sharing` is what needs one.
- Dependencies arrive through an `Options` struct with sensible zero values,
  not through package-level state.
- Comments explain decisions and constraints. If a comment restates the code,
  delete it.
- Errors are wrapped with `%w` and read as sentences. The user should learn what
  to do next: compare `tunnel: cloudflare requires "cloudflared", which is not
installed. Install it with brew install cloudflared` against `exec: not
found`.
- `gofmt` and `go vet` are not negotiable; `make check` enforces both.
