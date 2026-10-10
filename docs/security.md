# Security

vrok points a URL at files on your machine. This document says what that does
and does not protect.

## For teams evaluating vrok

vrok is a program you run, not a service. There is no account, no vrok server
and no stored copy of anything shared, so no vendor holds your data. What
matters is the route:

| Route                             | Who can see the traffic                                |
| --------------------------------- | ------------------------------------------------------ |
| Default (Cloudflare quick tunnel) | You, the visitor, and Cloudflare, which terminates TLS |
| `--local`                         | Anyone on your local network path                      |
| `--tunnel local`                  | Only this machine                                      |
| Your own relay                    | You, the visitor, and whoever runs the relay host      |

Releases ship SHA-256 checksums, an SPDX SBOM per archive and signed build
provenance (`gh attestation verify <file> --repo AliJabbar034/vrok`).
`govulncheck` runs on every change and weekly. The CLI sends no telemetry.

## Threat model

What vrok defends against:

- Someone guessing a share URL.
- A visitor with a valid URL reaching files that were not shared.
- A symlink inside a shared folder pointing out of it.
- A visitor probing which shares exist.
- Offline cracking of a share password.
- A shared file injecting markup or script into the viewer page.
- A share outliving the process that created it.
- A sender writing outside the receive folder, replacing a file in it, or
  sending files the owner did not accept.

What vrok does not defend against:

- Anyone the URL is given to. Possession of the URL is authorisation; that is
  the design.
- Your own machine being compromised. vrok serves what the user running it can
  read.
- A tunnel provider seeing your traffic. Cloudflare terminates TLS. So does a
  vrok relay. Share accordingly.
- Traffic analysis. A relay sees request sizes and timing even though it never
  sees a file at rest.
- What a received file contains. vrok checks names and sizes, not content. Treat
  a received file like any other download from someone you gave the link to.

## URLs

A share URL has two parts:

```
https://a82kd9.vrok.example.com/s/8kLmP3qR7wXz2vN4bYtJcA/
        ───┬──            ──────────┬───────────
         40-bit id            128-bit token
```

The **token** is 16 bytes from `crypto/rand`, base64url encoded. It is the
secret: knowing it is what authorises access. 128 bits is the floor for a value
that is the only thing between the internet and a local file.

The **id** is 5 bytes, encoded in lowercase base32 so it survives DNS, copy and
paste, and being read aloud. It is a routing key and a management handle, never
an authorisation. On a relay it becomes a hostname label, so the relay
validates it against `^[a-z0-9][a-z0-9-]{3,62}$` rather than trusting the agent.

There are no sequential ids anywhere. `/share/1` does not exist and never did.

## The visitor never names a file

This is the structural protection, and it matters more than any check:

```
GET /s/8kLmP3qR7wXz2vN4bYtJcA
       │
       ▼
   registry lookup  →  Share{Entries: [{Name: "video.mp4", Path: "/Users/ali/video.mp4"}]}
       │
       ▼
   /Users/ali/video.mp4
```

A visitor supplies a token, not a path. For single-file and multi-file shares,
the only addressable names are the ones vrok generated, matched against a list.
There is no route that accepts a filesystem path as a parameter, so there is no
`?path=` to attack.

Directory shares are the exception, because browsing a tree is the point. They
go through `security.PathResolver`, which is the only component allowed to turn
visitor input into a path.

## Path confinement

`PathResolver` checks containment twice.

**Lexically**, before touching the disk: the path is normalised with forward
slashes converted, cleaned, and rejected if it still climbs above its root or
contains a NUL byte. This costs nothing and cannot be fooled by a missing file.

**Physically**, after resolving symlinks: the real path must still live under
the real root. This is the check a string cleaner cannot make. Consider:

```sh
ln -s /Users/ali/.ssh ~/public/keys
vrok ~/public
```

`GET /s/<token>/keys/id_rsa` contains no `..` and cleans to a path inside the
root. Only resolving the link and re-checking containment catches it.

The root itself is canonicalised once, when the share is created, and cached.
That pins the share to the directory the user approved: replacing the root with
a symlink afterwards cannot redirect it.

Links that stay inside the share are allowed, because they are part of what was
shared.

Every byte that leaves the machine is opened through `security.OpenShared`,
which repeats the check at serve time. Sharing `demo.pdf` records that file's
canonical path. If the path is later replaced with a symlink to something else,
the next request 404s instead of following it. A live edit that keeps a regular
file at the same path still works.

A sibling in the same folder is not part of a single-file share. The visitor
can only request the names vrok generated. `secret.txt` next to `demo.pdf` is
not addressable.

Tested in `internal/security/security_test.go` and
`internal/server/server_test.go` against `../`, `..%2F`, `%2e%2e`, mixed
separators, absolute paths, escaping symlinks, sibling names, and a post-share
symlink swap.

## Passwords

```
password → Argon2id(t=1, m=64 MiB, p=4) → $argon2id$v=19$m=65536,t=1,p=4$<salt>$<key>
```

The plaintext exists for the duration of one function call. It is never stored
in the share, never logged, and never written to disk. The digest records its
own parameters, so cost can be raised later without invalidating existing
shares.

A successful unlock is remembered with a cookie containing
`HMAC-SHA256(process key, share token)`:

- The signing key is generated per process, so every cookie vrok ever issued
  becomes invalid when it exits. Cookies do not outlive shares.
- The cookie signs the share token, so it cannot be replayed against a
  different share.
- `HttpOnly`, so script cannot read it. `SameSite=Lax`, so a cross-site POST
  cannot use it. `Secure` when the visitor is on HTTPS. No expiry, so closing
  the browser forgets it.

The cookie is scoped to the share's path, `/s/<token>/`. A protected HTTP share
is the exception and uses `/`, because the app behind it may load absolute
paths like `/assets/app.js` that only reach the share through the Referer
fallback. That widens where the cookie is sent, not what it unlocks, and the
reverse proxy strips every `vrok_` cookie before a request reaches the app.

Comparisons are constant time. A failed attempt sleeps 400 ms, which combined
with the unguessable token makes online guessing pointless. At most two
verifications run at once and the rest queue: each one allocates 64 MiB, so a
burst of guesses must not be a way to exhaust the sharer's memory.

## Download limits

`--downloads N` is a hard limit on how many visitors receive the file. The
allowance is claimed before the file is opened, under one lock, so concurrent
requests cannot slip past it.

The hard part is ranged requests. A video player seeks with many of them, and
counting each would spend the allowance before one video finished. But a
`Range` header is whatever the client sends — `bytes=-N` returns the whole file
— so it cannot decide whether a request is free. Instead, the request that is
counted also gets a signed, `HttpOnly` cookie scoped to that one file. Later
requests for the same file that present it are free, including after the limit
is reached. Every other request is a new download, whatever its range.

A 304 or 416 delivers nothing, so it neither spends the allowance nor earns the
cookie. A client that hangs up after receiving the cookie has spent its
download, because the cookie is what lets it resume.

Reaching the limit stops new downloads at once. The share stays registered
until its last transfer finishes and has been quiet for 30 seconds, so the final
permitted download is never cut off and a paused video can resume. An expired
TTL ends transfers immediately.

The locked page shows no filename: a visitor without the password learns
nothing about what is behind it.

## Uniform failure

Every reason a share cannot be served produces a 404 with the same shape:

| Reason                                     | Response            |
| ------------------------------------------ | ------------------- |
| No such token                              | 404 "Not found"     |
| Expired                                    | 404 "Share expired" |
| Download limit reached                     | 404 "Share expired" |
| Revoked                                    | 404 "Share stopped" |
| Path escapes the root                      | 404 "Not found"     |
| Symlink loop, or a path that is not a file | 404 "Not found"     |
| File missing or unreadable                 | 404 "Not found"     |

Every failure to turn a request path into a servable file produces the same
page, byte for byte. A 500 would be as informative as a 403: it would tell a
prober that the path meant _something_ to the server. The reason is logged at
debug level only, since probes are expected traffic.

A 403 would confirm that something exists, which is already a disclosure. The
wording differs only where the visitor already knows — they had a working link
a moment ago.

Share pages are `Cache-Control: no-store` so a stopped share is not served from
a cache, and `noindex, nofollow, noarchive` so a temporary URL does not end up
in a search index.

## File content is untrusted

The viewer page treats a shared file as hostile input:

- Text and JSON previews are HTML-escaped. JSON is tokenised and highlighted
  server-side, so the page needs no third-party script.
- Markdown is rendered with raw HTML discarded.
- HTML files get their own preview inside `<iframe sandbox>` with no
  `allow-same-origin`, so the document runs in a unique origin.
- Previews read at most 2 MiB, archive listings at most 2000 entries, and
  directory listings at most 2000 rows. A page is built in memory before it is
  sent, so rendering must never be a way to make vrok allocate without bound —
  `vrok ./node_modules` would otherwise mean tens of megabytes of HTML per
  request. The cap is a rendering limit, not an access rule: a file left off
  the page is still reachable by name.
- Everything is served with `X-Content-Type-Options: nosniff` and an explicit
  `Content-Type` from vrok's own table, so the file's first bytes cannot change
  how a browser treats it.

The raw URL of a single-file or multi-file share can be opened directly, outside
the preview's sandboxed frame. HTML, SVG and XML served that way carry
`Content-Security-Policy: sandbox`, so they render in a unique origin with
scripts disabled.

Directory shares do serve HTML with scripts enabled, because serving a test
report or a built site is a primary use case and those artefacts need their own
scripts. The exposure is bounded: a script in a shared page is already inside
that share's origin, the unlock cookie is `HttpOnly`, and reaching another share
would require its 128-bit token.

## Receiving files

`vrok receive` is the one mode where a visitor writes to your disk, so it has
its own rules on top of everything above.

**You accept before anything is written.** The upload page first sends the
names and sizes it wants to send. vrok prints them and waits for `y` or `n`.
Declining, or not answering within five minutes, writes nothing. An accepted
batch is a list of permits, one per name and size, each spent by one upload: a
sender cannot swap in a bigger file, add files you never saw, or reuse a permit.
At most three batches wait at once, so a sender cannot flood the terminal.
`--yes` skips the question and accepts everything; it is meant for unattended
use and the banner says so.

**Names are sanitised, then confined.** `inbox.SafeName` keeps only the last
path element, drops control characters and the invisible characters that
reorder text (so `evil‮gpj.exe` cannot pose as a picture), replaces characters
Windows forbids,
strips leading dots so nothing lands hidden, and prefixes Windows device names
such as `CON`. The result is then opened through an `os.Root` for the folder,
so even a name that survived sanitising cannot follow a symlink or `../` out
of it.

**Nothing is overwritten.** A file is written to a hidden `.vrok-*.part` file
and renamed only when every declared byte has arrived. The final name is
reserved by creating it exclusively first, so a name that is taken, even by a
file that appeared a moment ago from somewhere else, becomes `photo (1).jpg`
instead. A part file can never be mistaken for a finished file, because
sanitised names never start with a dot.

**Sizes are enforced.** An upload declares its size. One byte more than that is
an error, and the excess never reaches the file. Before you are asked, the batch
must fit on the disk with 512 MiB to spare, and each file is checked again when
it starts. `--max-files` is claimed per file under the same lock as
`--downloads`, so concurrent senders cannot exceed it.

**Resources are bounded.** One chunk is at most 16 MiB, an offer at most 500
files, at most 64 offers are held at once, and at most 64 uploads are open at
once. Sizes are checked by subtraction, so a declared size near the 64-bit
limit cannot wrap around and pass the disk check.

**The page is locked down.** Upload requests must carry an `X-Vrok-Upload`
header. A cross-site form cannot set one, and a cross-site script cannot send
one without a CORS preflight that vrok never approves. The page is served with
a strict Content-Security-Policy (`default-src 'none'`, scripts and styles only
from vrok itself, `frame-ancestors 'none'`).

Received files are created with mode `0644` less your umask, like any other
download. When vrok stops, unfinished part files are deleted and finished files
stay where they are.

## Network exposure

The HTTP server binds `127.0.0.1` unless `--local` or `--listen` explicitly
asks for something else. Reaching it from anywhere else is a tunnel's job, not
the listener's, so a default share never listens on a public interface.

| Invocation                        | Reachable from                                     |
| --------------------------------- | -------------------------------------------------- |
| `vrok ./file`                     | The internet, via an automatically chosen provider |
| `vrok ./file --local`             | The local network                                  |
| `vrok ./file --tunnel local`      | This machine only                                  |
| `vrok ./file --tunnel <provider>` | The internet, via that provider                    |

The default is public because a link the recipient cannot open is not a share.
Two things keep that from being a trap: every banner states who can open the
URL, and the share still expires, still counts downloads and still carries a
128-bit token, so "public" means "whoever has the link, for the next two
hours" rather than "indexed and permanent".

`--local` always wins over a configured tunnel, because it is a statement about
privacy rather than a preference. `vrok config set tunnel local` makes a
private default permanent.

### Fetching a provider

When no tunnel client is installed, vrok downloads `cloudflared` into its cache
and runs it. That is executing a binary from the internet, so it is worth being
precise about:

- The version is pinned in `internal/tunnel/provision.go`, not resolved as
  "latest", so the binary is the one this release was tested against.
- It is fetched over HTTPS from Cloudflare's GitHub releases, authenticated by
  TLS to that host.
- Its SHA-256 is pinned per platform in the same file. TLS proves where the
  bytes came from, not that they are the bytes this release was tested with; a
  download that does not match is discarded and never executed.
- It is written to a temporary file and renamed into place, so an interrupted
  download cannot leave a partial binary that a later run would execute.
- Only a regular file named `cloudflared` is taken from the archive, so an
  archive naming a path outside the cache cannot write there.
- It is announced on the terminal while it happens.

To avoid it entirely, install a tunnel client yourself, configure a relay, or
use `--tunnel local`.

## Local state

| Path                                       | Mode   | Contents                         |
| ------------------------------------------ | ------ | -------------------------------- |
| `$XDG_STATE_HOME/vrok/sessions/`           | `0700` | One socket per running process   |
| `$XDG_STATE_HOME/vrok/sessions/<pid>.sock` | `0600` | Management API                   |
| `$XDG_CONFIG_HOME/vrok/config.json`        | `0600` | Defaults, possibly a relay token |

Reaching a session socket is enough to revoke someone's shares, which is why
both the directory and the sockets are owner-only. No share data, token or
password is ever written to disk. The only files vrok writes for a share are
the ones you accept into a receive folder.

## The relay

A relay operator can see request paths, headers and sizes, and can read the
bytes in flight. They cannot read a file that nobody requested, and nothing is
stored.

`/healthz` reports liveness and nothing else. How many agents are connected is
how many people are currently sharing, which is not an anonymous visitor's
business; operators read it from the relay's logs.

A share token does appear in the request path, so it is visible to the relay.
If that is unacceptable, use `--local`, a tunnel you control, or run your own
relay with `-token` set so only your agents can connect.

The agent endpoint accepts any origin, because agents are CLIs rather than
browsers: there is no cookie that a cross-site request could abuse.

## Reporting

Open an issue for anything that lets a URL reach bytes outside its share, lets
one share reach another, makes a stopped share keep answering, or lets a sender
write a file the receiver did not accept.
