//go:build unix

package agent

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Client talks to the agent socket. It implements vault.Decrypter.
type Client struct {
	socket  string
	timeout time.Duration
}

// NewClient returns a client for socket.
func NewClient(socket string) *Client { return &Client{socket: socket, timeout: 5 * time.Second} }

func (c *Client) call(req request) (response, error) {
	conn, err := net.DialTimeout("unix", c.socket, c.timeout)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return response{}, err
	}
	if !resp.OK {
		if resp.Locked {
			return resp, ErrLocked
		}
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// Status reports whether the agent is locked; an error means it is not running.
func (c *Client) Status() (locked bool, err error) {
	resp, err := c.call(request{Op: "status"})
	return resp.Locked, err
}

// Load gives the agent the key with an idle TTL (0 = none).
func (c *Client) Load(key string, ttl time.Duration) error {
	_, err := c.call(request{Op: "load", Key: key, TTL: int64(ttl / time.Second)})
	return err
}

// Lock makes the agent forget the key.
func (c *Client) Lock() error {
	_, err := c.call(request{Op: "lock"})
	return err
}

// Decrypt asks the agent to decrypt an age ciphertext.
func (c *Client) Decrypt(ct []byte) ([]byte, error) {
	resp, err := c.call(request{Op: "decrypt", Data: ct})
	return resp.Data, err
}

// Spawn starts "<self> agent" detached and waits until it answers. The
// agent outlives the caller, so it gets only an allowlisted env (agentEnv),
// never the caller's tokens.
func Spawn(self, socket string) error {
	cmd := exec.Command(self, "agent")
	cmd.Env = agentEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	c := NewClient(socket)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := c.Status(); err == nil {
			return nil
		}
	}
	return errors.New("agent did not start")
}
