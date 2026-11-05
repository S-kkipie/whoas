//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/awnumar/memguard"

	"github.com/S-kkipie/whoas/internal/vault"
)

const maxRequest = 16 << 20 // 16 MiB

// Server holds the key in locked memory and answers clients.
type Server struct {
	mu        sync.Mutex
	key       *memguard.Enclave
	ttl       time.Duration
	lastUse   time.Time
	now       func() time.Time
	reapEvery time.Duration
}

// NewServer returns a locked server.
func NewServer() *Server { return &Server{now: time.Now, reapEvery: 30 * time.Second} }

// Serve listens on socketPath (0600) until ctx is done.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	// Probe for an already-running agent.
	if _, err := NewClient(socketPath).Status(); err == nil {
		return fmt.Errorf("agent already running on %s", socketPath)
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
	// Start reaper goroutine.
	reapTicker := time.NewTicker(s.reapEvery)
	go func() {
		defer reapTicker.Stop()
		for {
			select {
			case <-reapTicker.C:
				s.mu.Lock()
				s.expireLocked()
				s.mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	// Listener cleanup goroutine.
	go func() {
		<-ctx.Done()
		ln.Close()
		_ = os.Remove(socketPath)
		s.mu.Lock()
		s.key = nil
		s.mu.Unlock()
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
	if err := json.NewDecoder(io.LimitReader(conn, maxRequest)).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(conn).Encode(s.do(req))
}

// expireLocked checks if the key should be expired due to idle TTL.
// Assumes s.mu is held.
func (s *Server) expireLocked() {
	if s.key != nil && s.ttl > 0 && s.now().Sub(s.lastUse) > s.ttl {
		s.key = nil
	}
}

func (s *Server) do(req request) response {
	switch req.Op {
	case "status":
		s.mu.Lock()
		s.expireLocked()
		locked := s.key == nil
		s.mu.Unlock()
		return response{OK: true, Locked: locked}
	case "lock":
		s.mu.Lock()
		s.key = nil
		s.mu.Unlock()
		return response{OK: true, Locked: true}
	case "load":
		// memguard protects the key at rest between requests; age needs a string
		// identity, so short-lived plaintext copies exist during load and decrypt
		// (same-user attackers are outside whoas's threat model).
		if _, err := age.ParseX25519Identity(req.Key); err != nil {
			return response{Error: "invalid key"}
		}
		s.mu.Lock()
		s.key = memguard.NewEnclave([]byte(req.Key))
		s.ttl = time.Duration(req.TTL) * time.Second
		s.lastUse = s.now()
		s.mu.Unlock()
		return response{OK: true}
	case "decrypt":
		// Decrypt outside the mutex: open the key under lock, then decrypt plaintext
		// outside the lock to minimize time holding the mutex.
		s.mu.Lock()
		s.expireLocked()
		if s.key == nil {
			s.mu.Unlock()
			return response{Error: ErrLocked.Error(), Locked: true}
		}
		buf, err := s.key.Open()
		if err != nil {
			s.mu.Unlock()
			return response{Error: err.Error()}
		}
		s.lastUse = s.now()
		s.mu.Unlock()
		// Outside the lock.
		d, err := vault.NewIdentityDecrypter(string(buf.Bytes()))
		buf.Destroy()
		if err != nil {
			return response{Error: err.Error()}
		}
		plain, err := d.Decrypt(req.Data)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, Data: plain}
	}
	return response{Error: "unknown op " + req.Op}
}
