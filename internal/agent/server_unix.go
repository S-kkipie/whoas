//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/awnumar/memguard"

	"github.com/S-kkipie/whoas/internal/vault"
)

// Server holds the key in locked memory and answers clients.
type Server struct {
	mu      sync.Mutex
	key     *memguard.Enclave
	ttl     time.Duration
	lastUse time.Time
	now     func() time.Time
}

// NewServer returns a locked server.
func NewServer() *Server { return &Server{now: time.Now} }

// Serve listens on socketPath (0600) until ctx is done.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(conn.(*net.UnixConn))
	}
}

func (s *Server) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := checkPeer(conn); err != nil {
		return
	}
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(conn).Encode(s.do(req))
}

func (s *Server) do(req request) response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil && s.ttl > 0 && s.now().Sub(s.lastUse) > s.ttl {
		s.key = nil
	}
	switch req.Op {
	case "status":
		return response{OK: true, Locked: s.key == nil}
	case "lock":
		s.key = nil
		return response{OK: true, Locked: true}
	case "load":
		if _, err := age.ParseX25519Identity(req.Key); err != nil {
			return response{Error: "invalid key"}
		}
		s.key = memguard.NewEnclave([]byte(req.Key))
		s.ttl = time.Duration(req.TTL) * time.Second
		s.lastUse = s.now()
		return response{OK: true}
	case "decrypt":
		if s.key == nil {
			return response{Error: ErrLocked.Error(), Locked: true}
		}
		buf, err := s.key.Open()
		if err != nil {
			return response{Error: err.Error()}
		}
		d, err := vault.NewIdentityDecrypter(string(buf.Bytes()))
		buf.Destroy()
		if err != nil {
			return response{Error: err.Error()}
		}
		plain, err := d.Decrypt(req.Data)
		if err != nil {
			return response{Error: err.Error()}
		}
		s.lastUse = s.now()
		return response{OK: true, Data: plain}
	}
	return response{Error: "unknown op " + req.Op}
}
