# Security Policy

## Supported versions

The latest release is supported. vrok is a single binary with no server-side
state to migrate, so the fix for anything reported here ships as a new patch
release rather than as a backport.

## Reporting a vulnerability

**Please do not open a public issue.**

Use GitHub's [private vulnerability reporting](https://github.com/AliJabbar034/vrok/security/advisories/new)
on the Security tab of this repository. That keeps the report private until a
fix is released, and it is the only channel that is guaranteed to reach a
maintainer.

Please include:

- what the issue lets an attacker do, and what they need to start
- the exact command and a minimal reproduction
- the output of `vrok --version` and your operating system

You will get an acknowledgement within three days and an assessment within
seven. If the report is valid you will be credited in the release notes unless
you ask not to be.

## What is in scope

[docs/security.md](docs/security.md) describes the threat model in full. In
short, vrok is meant to guarantee:

- **Path confinement.** A visitor cannot read anything outside the shared
  directory, including through `../`, symlinks that escape it, or encoded
  paths. The visitor never supplies a filesystem path.
- **Unguessable URLs.** Share tokens carry 128 bits of entropy from
  `crypto/rand`. Sequential or predictable identifiers are a bug.
- **Honest expiry.** An expired, revoked or exhausted share stops serving.
  Download limits cannot be bypassed by ranged requests.
- **Uniform failure.** Every reason a path is unavailable answers with the
  same 404, so probing reveals nothing about what exists.
- **Password handling.** Passwords are stored as Argon2id digests, never in
  plaintext, and never written to disk or logs.
- **No silent exposure.** The listener binds loopback. Reaching it from
  elsewhere is always a tunnel's job, and the banner states who can reach it.

A break in any of those is a vulnerability. Please report it.

## What is out of scope

- **A share being public.** That is the purpose of the tool. Anyone with the
  link can open it until it expires.
- **Content served from a shared directory.** If you share a directory
  containing a secret, the secret is shared. vrok confines access to the
  directory you chose; it does not judge what is in it.
- **HTML and JavaScript inside shared files.** Serving a report or a built
  site is a primary use case. Scripts run in that share's own origin; the
  unlock cookie is `HttpOnly` and reaching another share needs its token.
- **Third-party tunnel providers.** Issues in `cloudflared` belong to
  Cloudflare. How vrok fetches and runs it is in scope.
- **Anything requiring local access to the sharing machine.** A user who can
  read your filesystem or your process memory has already won.
