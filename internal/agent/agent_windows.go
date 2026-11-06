//go:build windows

package agent

import (
	"context"
	"errors"
	"time"
)

// errUnsupported is returned on platforms without the agent.
var errUnsupported = errors.New("whoas agent is not supported on this platform")

// Server is unavailable on Windows (whoas.exe only serves keystore-helper).
type Server struct{}

func NewServer() *Server                              { return &Server{} }
func (s *Server) Serve(context.Context, string) error { return errUnsupported }

// Client is unavailable on Windows.
type Client struct{}

func NewClient(string) *Client                     { return &Client{} }
func (c *Client) Status() (bool, error)            { return true, errUnsupported }
func (c *Client) Load(string, time.Duration) error { return errUnsupported }
func (c *Client) Lock() error                      { return errUnsupported }
func (c *Client) Decrypt([]byte) ([]byte, error)   { return nil, errUnsupported }
func Spawn(string, string) error                   { return errUnsupported }
