//go:build windows

package runlock

// Alive is conservative on Windows, where running CLIs is not supported yet.
func Alive(pid int) bool { return true }
