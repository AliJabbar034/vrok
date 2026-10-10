# vrok

## What it is

A single-binary CLI that shares local files, folders and development servers
through temporary public URLs. Nothing is uploaded and nothing is stored:
requests are served from the user's own machine while the command runs. It
also works the other way round: `vrok receive` gives a link that lets someone
send files straight into a folder on the user's machine.

```
$ vrok ./video.mp4
✓ Sharing video.mp4

  URL:         https://avi-downtown-justin-postings.trycloudflare.com/s/8kLmP3qR7wXz2vN4bYtJcA/
  Reachable:   anyone with the link
  Expires:     2h
  Tunnel:      cloudflare
```

## Unique mechanism

One command turns a path into a public URL that expires. The file never
leaves the machine, and the link dies on a timer, a download count, or
Ctrl+C. Competitors expose a _port_ indefinitely; vrok creates a _share_ with
a lifetime.

## Audience

Developers and designers who need to hand someone a file or a running
localhost **right now**: a 300 MB video that will not fit in Slack, a
Playwright report, a staging build for a client, a dev server for a
colleague across the country. They live in a terminal and judge a tool by how
fast it gets out of the way.

## What this landing page must prove

That it is one command, that the URL is real and public, and that it expires.
The proof is the actual terminal banner, not a description of it.

## Facts that are true and may be stated

- One command, no account, no signup, no configuration.
- Nothing is uploaded; bytes are read from the user's disk per request.
- Default URL is public via a Cloudflare quick tunnel; `cloudflared` is
  fetched automatically if absent.
- Shares expire (2h default), can cap downloads, and can require a password.
- Share tokens carry 128 bits of entropy from `crypto/rand`.
- Works for any file type, whole directories, several files at once, and
  `localhost:PORT` as a reverse proxy with WebSocket/hot-reload support.
- Range requests, video seeking and resumable downloads are supported.
- `vrok receive` opens an upload page; files land in `~/Downloads/vrok` by
  default. The owner sees each batch's names and sizes and presses `y` before
  anything is written. Existing files are never overwritten, uploads resume,
  and there is no size limit beyond free disk space.
- Runs on macOS, Linux and Windows, amd64 and arm64.
- Install: shell one-liner, PowerShell one-liner, `go install`, or a release
  archive. Homebrew/Scoop/winget exist once the tap repos are created.
- MIT licensed. Source at https://github.com/AliJabbar034/vrok

## Claims that may NOT be made

No user counts, download counts, company logos, testimonials, benchmarks,
uptime figures, funding, team size, or awards. None of these exist.

## Deliberate non-goals

No accounts, no database, no cloud storage, no billing, no teams, no
analytics platform, no admin dashboard, no permanent URLs. The core idea is
temporary access to something on your own machine.

## Brand commitments

Name is lowercase `vrok` everywhere. The terminal banner's glyphs (`✓`, `■`)
and its label/value alignment are part of the product's identity.
