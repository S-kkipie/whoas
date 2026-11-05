//go:build unix && !linux && !darwin

package agent

import "net"

// checkPeer relies on the socket's 0600 mode and 0700 parent dir here.
func checkPeer(*net.UnixConn) error { return nil }
