package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dialTimeout bounds how long a management command waits for one session. A
// socket in the state directory is local, so anything slower is a wedged
// process we should skip rather than hang on.
const dialTimeout = 2 * time.Second

// ErrNoSessions reports that no vrok process is currently sharing.
var ErrNoSessions = errors.New("control: no active vrok sessions")

// Session is a reachable vrok process.
type Session struct {
	PID    int
	Socket string
}

// Sessions lists the running vrok processes, cleaning up sockets left behind
// by processes that died without removing them.
func Sessions() ([]Session, error) {
	dir, err := sessionDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("control: read %s: %w", dir, err)
	}

	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sock") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".sock"))
		if err != nil {
			continue
		}
		socket := filepath.Join(dir, entry.Name())

		// A socket whose process is gone is stale. Probing it is the only
		// reliable test: a pid can be reused, and a signal check cannot tell
		// whether the process is still vrok.
		if !reachable(socket) {
			os.Remove(socket)
			continue
		}
		sessions = append(sessions, Session{PID: pid, Socket: socket})
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].PID < sessions[j].PID })
	return sessions, nil
}

func reachable(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, dialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Client talks to one vrok session.
type Client struct {
	session Session
	http    *http.Client
}

// Dial returns a client for a session.
func Dial(session Session) *Client {
	return &Client{
		session: session,
		http: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				// The host in the URL is ignored: every request goes to this
				// one socket.
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "unix", session.Socket)
				},
			},
		},
	}
}

// PID returns the process id this client talks to.
func (c *Client) PID() int { return c.session.PID }

// Shares returns the session's live shares.
func (c *Client) Shares(ctx context.Context) ([]ShareInfo, error) {
	var shares []ShareInfo
	if err := c.call(ctx, http.MethodGet, pathShares, &shares); err != nil {
		return nil, err
	}
	for i := range shares {
		shares[i].PID = c.session.PID
	}
	return shares, nil
}

// Revoke stops one share, reporting whether this session owned it.
func (c *Client) Revoke(ctx context.Context, id string) (bool, error) {
	var result struct {
		Revoked bool `json:"revoked"`
	}
	if err := c.call(ctx, http.MethodPost, pathRevoke+"/"+id, &result); err != nil {
		return false, err
	}
	return result.Revoked, nil
}

// Stop asks the session to exit.
func (c *Client) Stop(ctx context.Context) error {
	var ignored json.RawMessage
	return c.call(ctx, http.MethodPost, pathStop, &ignored)
}

func (c *Client) call(ctx context.Context, method, path string, out any) error {
	// The URL host is a placeholder; the transport always dials the socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://vrok"+path, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("control: session %d: %w", c.session.PID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("control: session %d returned %s: %s", c.session.PID, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// AllShares collects the shares of every running session.
//
// A session that fails to answer is skipped rather than failing the whole
// command: one wedged process must not hide every other share.
func AllShares(ctx context.Context) ([]ShareInfo, error) {
	sessions, err := Sessions()
	if err != nil {
		return nil, err
	}

	var all []ShareInfo
	for _, session := range sessions {
		shares, err := Dial(session).Shares(ctx)
		if err != nil {
			continue
		}
		all = append(all, shares...)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	return all, nil
}
