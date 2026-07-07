package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// DefaultDialTimeout bounds how long Dial waits to connect before failing.
const DefaultDialTimeout = 3 * time.Second

// Client is a thin dialer for the daemon control socket. It is the intended
// entry point for the clex CLI (#17): construct one with the socket path, then
// call the typed command helpers. Each call opens a fresh connection, sends one
// framed Request, reads one framed Response, and closes — matching the server's
// one-shot connection model, so a Client is safe to reuse and share.
type Client struct {
	path        string
	dialTimeout time.Duration
}

// NewClient returns a Client that dials the control socket at path.
func NewClient(path string) *Client {
	return &Client{path: path, dialTimeout: DefaultDialTimeout}
}

// WithDialTimeout overrides the connect timeout (tests use a short one).
func (c *Client) WithDialTimeout(d time.Duration) *Client {
	c.dialTimeout = d
	return c
}

// Do sends req and returns the daemon's Response. It stamps the protocol
// version on the outgoing request so callers never have to. A transport-level
// failure (cannot connect, cannot read) is returned as an error; a command that
// the daemon rejected comes back as a Response with OK=false and no error.
func (c *Client) Do(ctx context.Context, req Request) (Response, error) {
	req.Version = ProtocolVersion
	d := net.Dialer{Timeout: c.dialTimeout}
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return Response{}, fmt.Errorf("ipc: dial %s: %w", c.path, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("ipc: marshal request: %w", err)
	}
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		return Response{}, fmt.Errorf("ipc: write request: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Response{}, fmt.Errorf("ipc: read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("ipc: unmarshal response: %w", err)
	}
	if resp.Version != ProtocolVersion {
		return resp, fmt.Errorf("ipc: protocol version mismatch: cli=%d daemon=%d", ProtocolVersion, resp.Version)
	}
	return resp, nil
}

// Status requests a daemon snapshot.
func (c *Client) Status(ctx context.Context) (Response, error) {
	return c.Do(ctx, Request{Command: CmdStatus})
}

// Pause sets the global pause flag.
func (c *Client) Pause(ctx context.Context) (Response, error) {
	return c.Do(ctx, Request{Command: CmdPause})
}

// Resume clears the global pause flag.
func (c *Client) Resume(ctx context.Context) (Response, error) {
	return c.Do(ctx, Request{Command: CmdResume})
}

// Stop cancels the runner for the given issue, reverts its label, and preserves
// its worktree.
func (c *Client) Stop(ctx context.Context, issue int) (Response, error) {
	return c.Do(ctx, Request{Command: CmdStop, Issue: issue})
}

// Steer injects steering text toward an issue (issue 0 targets the epic).
func (c *Client) Steer(ctx context.Context, issue int, text string) (Response, error) {
	return c.Do(ctx, Request{Command: CmdSteer, Issue: issue, Text: text})
}

// Models reports registry health.
func (c *Client) Models(ctx context.Context) (Response, error) {
	return c.Do(ctx, Request{Command: CmdModels})
}

// Costs reports spend.
func (c *Client) Costs(ctx context.Context) (Response, error) {
	return c.Do(ctx, Request{Command: CmdCosts})
}
