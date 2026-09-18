// Package browser opens a URL in the platform's default browser. It is used by
// the console and the system-tray front ends.
package browser

import (
	"os/exec"
	"runtime"
)

// Open best-effort launches the platform browser at url. Failures are silent:
// the URL is always shown to the user so they can open it themselves. The
// command runs in the background and is reaped so it leaves no zombie.
func Open(url string) {
	if url == "" {
		return
	}
	name, args := command()
	if name == "" {
		return
	}
	go func() { _ = exec.Command(name, append(args, url)...).Run() }()
}

// command names the platform's URL opener. A leading argument list, if any, is
// passed before the URL.
func command() (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		return "xdg-open", nil
	}
}
