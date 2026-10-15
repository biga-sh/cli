// Package browser opens URLs in the user's default browser.
package browser

import (
	"errors"
	"net/url"
	"os/exec"
	"runtime"
)

// Open opens an http or https URL in the default browser. It returns once
// the opener has been started, without waiting for the browser.
func Open(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	// Only web URLs: the opener would also run file: and custom schemes.
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("browser: only http and https URLs can be opened")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u.String())
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String())
	default:
		cmd = exec.Command("xdg-open", u.String())
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the opener in the background; it exits as soon as it hands off.
	go func() { _ = cmd.Wait() }()
	return nil
}
