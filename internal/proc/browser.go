package proc

import (
	"os/exec"
	"runtime"
)

// OpenURL launches the OS default handler for a URL without blocking the
// caller. It is best-effort: sign-in flows call it as a convenience and still
// show the URL, because there is no handler to launch on a headless box or
// over SSH.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
