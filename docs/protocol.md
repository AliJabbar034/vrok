# Relay protocol

Version 1. Implemented by `internal/protocol`, spoken by `internal/relay`
(server) and `internal/tunnel`'s relay provider (agent).

## Design rule

The relay carries HTTP semantics without interpreting them. Methods, headers,
status codes, `Range`, `ETag`, `Content-Length` and `Last-Modified` all cross
the tunnel unchanged, which is what keeps video seeking, resumable downloads,
caching and correct previews working on a public URL.

The relay never stores a file. A visitor's download is read off the sharing
machine's disk while they wait.

## Transport

One WebSocket connection per sharing process:

```
wss://relay.example.com/_vrok/agent
```

Two kinds of message travel over it:

- **Text frames** carry JSON control messages.
- **Binary frames** carry request and response bodies.

Bodies are binary rather than base64 inside JSON because base64 would inflate
every byte of every download by a third, and because a binary frame can be
forwarded straight into an `io.Writer`.

A WebSocket permits only one writer at a time, which is easy to violate once
several HTTP exchanges share one connection. `protocol.Conn` serialises every
write behind a mutex so no caller has to remember that.

## Control messages

```json
{ "type": "request", "payload": { ... } }
```

| Type         | Direction     | Purpose                                            |
| ------------ | ------------- | -------------------------------------------------- |
| `register`   | agent → relay | Claim a hostname label. Must be the first message. |
| `registered` | relay → agent | Confirm, and report the public URL.                |
| `request`    | relay → agent | Start a proxied request.                           |
| `response`   | agent → relay | Status line and headers.                           |
| `cancel`     | either        | Abandon one stream.                                |
| `error`      | either        | Report a failure, per stream or fatal.             |

### register

```json
{
  "version": 1,
  "share_id": "a82kd9",
  "token": "8kLmP3qR7wXz2vN4bYtJcA",
  "auth": "optional relay credential",
  "agent": "vrok"
}
```

The relay validates `share_id` against `^[a-z0-9][a-z0-9-]{3,62}$` before
accepting it, because the value becomes part of a hostname. `token` is echoed
back by the agent's own bookkeeping; the relay does not inspect it and cannot
use it to reach the share.

A version mismatch is refused rather than guessed at.

### registered

```json
{ "url": "https://a82kd9.example.com", "hostname": "a82kd9.example.com" }
```

The relay returns an origin, not a share URL. The CLI appends the share path
itself, so one tunnel could serve several shares.

### request

```json
{
  "stream": 7,
  "method": "GET",
  "uri": "/s/8kLmP3qR7wXz2vN4bYtJcA/video.mp4?raw=1",
  "header": { "Range": ["bytes=1048576-"] },
  "remote_addr": "203.0.113.7:51234",
  "has_body": false
}
```

`uri` is the request target including the query string, exactly as the visitor
sent it. Stream ids are allocated by the relay and are unique per connection.

Hop-by-hop headers (`Connection`, `Keep-Alive`, `Transfer-Encoding`,
`Upgrade`, `TE`, `Trailer`, `Proxy-*`) are stripped in both directions: they
describe one TCP hop, and forwarding them would make the far side frame the
message twice. `X-Forwarded-For`, `X-Forwarded-Proto` and `X-Forwarded-Host`
are added on the way in.

### response

```json
{ "stream": 7, "status": 206, "header": { "Content-Range": ["bytes 1048576-2097151/5242880"] } }
```

Exactly one response per stream, followed by body frames.

## Binary frames

```
 0        8         9                              n
 ├────────┼─────────┼──────────────────────────────┤
 │ stream │  flags  │            payload           │
 │ uint64 │  uint8  │       ≤ 32768 bytes          │
 └────────┴─────────┴──────────────────────────────┘

flags: bit 0 = end of stream
```

The payload cap matches the buffer `io.Copy` uses, so bodies are forwarded
without re-chunking. A frame may be both final and empty, which is how a
zero-length body ends.

Both directions use the same format: relay to agent for request bodies, agent
to relay for response bodies.

## A download, start to finish

```
visitor            relay                     agent              disk
   │                 │                         │                  │
   │ GET …/video.mp4 │                         │                  │
   │  Range: 1MB-    │                         │                  │
   ├────────────────►│                         │                  │
   │                 │ {"type":"request",…}    │                  │
   │                 ├────────────────────────►│                  │
   │                 │                         │ GET 127.0.0.1    │
   │                 │                         ├─────────────────►│
   │                 │                         │   206 + bytes    │
   │                 │ {"type":"response",206} │◄─────────────────┤
   │                 │◄────────────────────────┤                  │
   │  206 + headers  │                         │                  │
   │◄────────────────┤  [frame stream=7 32KB]  │                  │
   │  32 KB          │◄────────────────────────┤                  │
   │◄────────────────┤           …             │                  │
   │                 │  [frame stream=7 end]   │                  │
   │  (complete)     │◄────────────────────────┤                  │
```

If the visitor disconnects mid-download, the relay sends `cancel` so the agent
stops reading the file rather than streaming into a closed socket.

## Flow control and liveness

Each stream has a bounded queue of 32 frames (1 MiB). A visitor slower than
that applies backpressure up the chain instead of letting the relay buffer
without limit. The bound is also the limit of the isolation: one connection is
one TCP stream, so a slow reader can still slow its neighbours.

Both ends ping every 20 seconds and expect traffic within 60, so a tunnel that
has silently died is detected instead of being advertised as live.

## Error handling

| Situation                       | Behaviour                                                                               |
| ------------------------------- | --------------------------------------------------------------------------------------- |
| Label already claimed           | `error`, then close. The CLI reports it.                                                |
| Protocol version mismatch       | `error`, then close.                                                                    |
| Bad or missing relay credential | `error`, then close.                                                                    |
| Local server does not answer    | `error` scoped to the stream; the visitor sees 502.                                     |
| Agent disconnects mid-download  | The visitor's transfer is truncated, like any dropped HTTP connection.                  |
| Relay restarts                  | Every tunnel drops. There is nothing to recover: the files are on the agents' machines. |
| Unknown hostname                | 404, identical to a stopped share.                                                      |

## Not carried

Protocol upgrades. A WebSocket handshake through the relay returns 501 rather
than stalling. Carrying it would mean turning a request/response channel into a
bidirectional byte stream on both ends, which is work queued behind moving the
transport to QUIC.
