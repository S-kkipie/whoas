//go:build unix

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// spawn runs the real CLI and waits. SIGTERM/SIGHUP are always forwarded.
// SIGINT/SIGQUIT are forwarded only when whoas is not in a terminal's
// foreground process group; otherwise the terminal already delivered them
// to the child and forwarding would double them.
func (p *Plan) spawn() (Result, error) {
	forwardAll := !terminalDelivers()
	argv := append(p.Adapter.InjectArgs(p.RunBase), p.Args...)
	cmd := exec.Command(p.Bin, argv...)
	cmd.Env = append(append([]string{}, p.Env...), p.Adapter.InjectEnv(p.RunBase)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.Stdin, p.Stdout, p.Stderr

	sigs := make(chan os.Signal, 4)
	var watch []os.Signal
	for _, s := range []os.Signal{syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(s) {
			watch = append(watch, s)
		}
	}
	if len(watch) > 0 {
		signal.Notify(sigs, watch...)
	}
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("%w: %s: %w", ErrStart, p.Bin, err)
	}
	waited := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if forwardAll || s == syscall.SIGTERM || s == syscall.SIGHUP {
					_ = cmd.Process.Signal(s)
				}
			case <-waited:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(waited)
	return resultOf(err)
}

// terminalDelivers reports whether the controlling terminal's foreground
// process group is ours, so Ctrl-C already reaches the child.
func terminalDelivers() bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	defer tty.Close()
	pgrp, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCGPGRP)
	return err == nil && pgrp == syscall.Getpgrp()
}

func resultOf(err error) (Result, error) {
	if err == nil {
		return Result{}, nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return Result{}, err
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return Result{Code: 128 + int(ws.Signal()), Signal: ws.Signal()}, nil
	}
	return Result{Code: ee.ExitCode()}, nil
}

// Reraise makes this process die from sig so its parent sees the same status.
// For SIGHUP, SIGINT or SIGTERM: it resets the signal, kills the process, and waits.
// For other signals (e.g. QUIT, ABRT, PIPE) it returns at once and the caller exits 128+n,
// because Go's runtime would turn them into a stack dump.
func Reraise(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	switch s {
	case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM:
		signal.Reset(s)
		_ = syscall.Kill(os.Getpid(), s)
		time.Sleep(100 * time.Millisecond)
	}
}
