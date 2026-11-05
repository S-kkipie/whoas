package keystore

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// ttyPrompt reads a hidden line from the controlling terminal, so it works
// even when stdin is piped.
func ttyPrompt(label string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("passphrase needed but no terminal (set WHOAS_PASSPHRASE): %w", err)
	}
	defer tty.Close()
	fmt.Fprint(tty, label)
	b, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
