package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runner executes whoas.exe; code is the exit code, err only for spawn failures.
type runner func(args []string, stdin string) (stdout string, code int, err error)

// wslStore keeps the key in Windows Credential Manager by calling the
// Windows build of whoas (whoas.exe keystore-helper) through WSL interop.
type wslStore struct {
	exe string
	run runner
}

func newWSLStore(exe string) *wslStore { return &wslStore{exe: exe, run: execRunner(exe)} }

func (s *wslStore) Name() string       { return "wsl (Windows Credential Manager via " + s.exe + ")" }
func (s *wslStore) Slow() bool         { return true }
func (s *wslStore) TTL() time.Duration { return 0 }

func (s *wslStore) Get() (string, error) {
	out, code, err := s.run([]string{"keystore-helper", "get"}, "")
	if err != nil {
		return "", err
	}
	switch code {
	case 0:
		return strings.TrimSpace(out), nil
	case 2:
		return "", ErrNotFound
	default:
		return "", fmt.Errorf("%s keystore-helper get: exit %d", s.exe, code)
	}
}

func (s *wslStore) Set(secret string) error {
	_, code, err := s.run([]string{"keystore-helper", "set"}, secret)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s keystore-helper set: exit %d", s.exe, code)
	}
	return nil
}

func execRunner(exe string) runner {
	return func(args []string, stdin string) (string, int, error) {
		cmd := exec.Command(exe, args...)
		cmd.Stdin = strings.NewReader(stdin)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.String(), ee.ExitCode(), nil
		}
		if err != nil {
			return "", -1, err
		}
		return out.String(), 0, nil
	}
}

func isWSL(getenv func(string) string) bool {
	if getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	return err == nil && bytes.Contains(bytes.ToLower(b), []byte("microsoft"))
}

// findWinExe locates whoas.exe: $WHOAS_WIN_EXE, else next to this binary.
func findWinExe(getenv func(string) string) (string, error) {
	if p := getenv("WHOAS_WIN_EXE"); p != "" {
		return p, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	cand := filepath.Join(filepath.Dir(self), "whoas.exe")
	if _, err := os.Stat(cand); err != nil {
		return "", errors.New("whoas.exe not found next to whoas (set WHOAS_WIN_EXE)")
	}
	return cand, nil
}
