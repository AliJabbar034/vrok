// Package protocol defines the wire format spoken between the vrok CLI and the
// vrok relay.
//
// The design rule is that the relay is a router, not a store: it forwards HTTP
// semantics verbatim and never holds a file. Requests and responses keep their
// methods, headers, status codes and Range handling, so a browser talking to
// the relay behaves exactly as it would talking to the CLI directly — which is
// what preserves video seeking, resumable downloads and caching.
package protocol

import (
	"encoding/json"
	"fmt"
)

// Version is the protocol revision. The relay rejects agents it cannot speak
// to rather than guessing.
const Version = 1

// AgentPath is the relay endpoint where agents open their tunnel.
const AgentPath = "/_vrok/agent"

// Type identifies a control message.
type Type string

// Control message types.
const (
	// TypeRegister claims a hostname. Agent to relay, first message.
	TypeRegister Type = "register"
	// TypeRegistered confirms the claim and reports the public URL.
	TypeRegistered Type = "registered"
	// TypeRequest starts a proxied request. Relay to agent.
	TypeRequest Type = "request"
	// TypeResponse returns status and headers. Agent to relay.
	TypeResponse Type = "response"
	// TypeCancel abandons a stream. Either direction.
	TypeCancel Type = "cancel"
	// TypeError reports a failure, either fatal to the connection or scoped
	// to one stream.
	TypeError Type = "error"
)

// Register claims a hostname label on the relay.
type Register struct {
	Version int `json:"version"`
	// ShareID becomes the hostname label, e.g. "a82kd9" in
	// https://a82kd9.example.com.
	ShareID string `json:"share_id"`
	// Token is the share secret. The relay never inspects it; it is echoed
	// back so the agent can confirm the relay is talking about its share.
	Token string `json:"token"`
	// Auth is an optional relay credential for private deployments.
	Auth string `json:"auth,omitempty"`
	// Agent identifies the client build, for diagnostics.
	Agent string `json:"agent,omitempty"`
}

// Registered confirms a successful registration.
type Registered struct {
	// URL is the public origin the share is now reachable on.
	URL string `json:"url"`
	// Hostname is the full hostname the relay assigned.
	Hostname string `json:"hostname"`
}

// Request describes an inbound HTTP request the relay wants served.
type Request struct {
	// Stream correlates this request with its body and response frames.
	Stream uint64 `json:"stream"`
	Method string `json:"method"`
	// URI is the request target including the query string, exactly as the
	// visitor sent it.
	URI    string              `json:"uri"`
	Header map[string][]string `json:"header,omitempty"`
	// RemoteAddr is the visitor's address, forwarded for logging only.
	RemoteAddr string `json:"remote_addr,omitempty"`
	// HasBody tells the agent whether to expect body frames.
	HasBody bool `json:"has_body"`
}

// Response carries the status line and headers of a reply.
type Response struct {
	Stream uint64              `json:"stream"`
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
}

// Cancel abandons a stream, typically because a visitor disconnected.
type Cancel struct {
	Stream uint64 `json:"stream"`
	Reason string `json:"reason,omitempty"`
}

// Error reports a failure. A zero Stream means the whole connection failed.
type Error struct {
	Stream  uint64 `json:"stream,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// Envelope wraps a control message with its type so the receiver can decode
// the payload into the right struct.
type Envelope struct {
	Type    Type            `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Encode marshals a control message into an envelope.
func Encode(t Type, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode %s: %w", t, err)
	}
	return json.Marshal(Envelope{Type: t, Payload: raw})
}

// Decode unmarshals an envelope.
func Decode(data []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return Envelope{}, fmt.Errorf("protocol: decode envelope: %w", err)
	}
	if e.Type == "" {
		return Envelope{}, fmt.Errorf("protocol: envelope has no type")
	}
	return e, nil
}

// Payload decodes an envelope's payload into T.
func Payload[T any](e Envelope) (T, error) {
	var value T
	if len(e.Payload) == 0 {
		return value, fmt.Errorf("protocol: %s message has no payload", e.Type)
	}
	if err := json.Unmarshal(e.Payload, &value); err != nil {
		return value, fmt.Errorf("protocol: decode %s payload: %w", e.Type, err)
	}
	return value, nil
}
