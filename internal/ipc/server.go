package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// SocketPath returns the control socket path for a given clex home directory.
func SocketPath(home string) string { return filepath.Join(home, SocketName) }

// Handler executes control requests. The daemon implements it; the server owns
// only transport, framing, and versioning. A Handler must be safe for
// concurrent use — the server may invoke it from multiple connection
// goroutines, though in practice control traffic is low and serial.
type Handler interface {
	// Handle processes one request and returns the response payload. It must not
	// set Response.Version — the server stamps it. Returning an error is
	// equivalent to a Response with OK=false and that error's text; prefer
	// returning a populated Response for expected, user-facing failures.
	Handle(ctx context.Context, req Request) (Response, error)
}

// Server accepts control connections on a Unix-domain socket and dispatches
// each to a Handler. Construct with Listen; run with Serve; release with Close.
type Server struct {
	ln   net.Listener
	h    Handler
	log  *slog.Logger
	path string

	mu     sync.Mutex
	closed bool
}

// Listen creates the control socket at path with mode 0600 and returns a Server
// bound to h. Any stale socket left by a previous run is removed first (a
// crashed daemon leaves the file behind; binding would otherwise fail with
// "address already in use"). The parent directory is expected to already exist
// with restrictive permissions (the daemon creates ~/.clex 0700).
//
// The 0600 mode is applied explicitly after bind rather than relying on the
// process umask, satisfying the spec's "IPC socket is 0600" requirement
// regardless of the caller's umask.
func Listen(path string, h Handler, log *slog.Logger) (*Server, error) {
	if h == nil {
		return nil, errors.New("ipc: nil handler")
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	// Remove a stale socket from a prior crash. Ignore "not exist"; surface
	// anything else (e.g. a regular file we must not clobber blindly).
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen %s: %w", path, err)
	}
	// Tighten permissions explicitly; net.Listen honors umask, which we do not
	// trust to be restrictive. 0600 = owner read/write only.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("ipc: chmod socket: %w", err)
	}
	return &Server{ln: ln, h: h, log: log, path: path}, nil
}

// removeStaleSocket deletes a leftover socket file so a fresh Listen can bind.
// It refuses to delete a path that exists but is not a socket, to avoid
// destroying an unrelated file the operator may have placed there.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ipc: stat socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("ipc: %s exists and is not a socket; refusing to remove", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("ipc: remove stale socket: %w", err)
	}
	return nil
}

// Serve accepts connections until ctx is cancelled or Close is called. It
// returns nil on a clean shutdown (ctx cancellation or Close) and a non-nil
// error only on an unexpected accept failure. Each connection is handled in its
// own goroutine.
func (s *Server) Serve(ctx context.Context) error {
	// Close the listener when ctx is cancelled so a blocked Accept unblocks.
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if s.isClosed() || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("ipc: accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		s.log.Debug("ipc: read request", "err", err)
		return
	}
	var req Request
	resp := Response{Version: ProtocolVersion}
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		resp.OK = false
		resp.Error = "malformed request: " + uerr.Error()
		s.writeResponse(conn, resp)
		return
	}
	if req.Version != ProtocolVersion {
		resp.OK = false
		resp.Error = fmt.Sprintf("protocol version mismatch: daemon=%d cli=%d", ProtocolVersion, req.Version)
		s.writeResponse(conn, resp)
		return
	}
	out, herr := s.h.Handle(ctx, req)
	out.Version = ProtocolVersion
	if herr != nil {
		out.OK = false
		if out.Error == "" {
			out.Error = herr.Error()
		}
	}
	s.writeResponse(conn, out)
}

func (s *Server) writeResponse(conn net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("ipc: marshal response", "err", err)
		return
	}
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		s.log.Debug("ipc: write response", "err", err)
	}
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Close stops accepting connections and removes the socket file. It is
// idempotent and safe to call from multiple goroutines.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	err := s.ln.Close()
	// Best-effort unlink; the socket path is ours to clean up.
	_ = os.Remove(s.path)
	return err
}
