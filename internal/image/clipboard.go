package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/genai-io/san/internal/core"
)

// newClipboardImage builds a core.Image from clipboard PNG data.
// Returns nil, nil if data is empty.
func newClipboardImage(data []byte) (*core.Image, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) > maxImageSize {
		return nil, fmt.Errorf("clipboard image too large: %d bytes (max %d)", len(data), maxImageSize)
	}
	fileName := fmt.Sprintf("clipboard_%s.png", time.Now().Format("150405"))
	img := newImage("image/png", fileName, "", data)
	return &img, nil
}

// readClipboardMacOS reads image from macOS clipboard using osascript.
func readClipboardMacOS() (*core.Image, error) {
	tmp, err := os.CreateTemp("", "clipboard_*.png")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpFile := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpFile) }()

	script := fmt.Sprintf(`
		set theFile to POSIX file "%s"
		try
			set imgData to the clipboard as «class PNGf»
			set fileRef to open for access theFile with write permission
			write imgData to fileRef
			close access fileRef
			return "ok"
		on error
			return "no image"
		end try
	`, tmpFile)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read clipboard: %w", err)
	}

	if strings.TrimSpace(string(output)) == "no image" {
		return nil, nil
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read clipboard image: %w", err)
	}

	return newClipboardImage(data)
}

// readClipboardLinux reads image from Linux clipboard using xclip or xsel.
func readClipboardLinux() (*core.Image, error) {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	data, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("xsel", "--clipboard", "--output")
		data, err = cmd.Output()
		if err != nil {
			return nil, nil
		}
	}
	return newClipboardImage(data)
}

// readClipboardWindows reads image from the Windows clipboard using PowerShell.
func readClipboardWindows() (*core.Image, error) {
	tmp, err := os.CreateTemp("", "clipboard_*.png")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpFile := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpFile) }()

	// Clipboard.GetImage requires a single-threaded apartment (-STA). It returns
	// $null when the clipboard holds no image; otherwise we save it as PNG.
	script := fmt.Sprintf(`
		Add-Type -AssemblyName System.Windows.Forms,System.Drawing
		$img = [System.Windows.Forms.Clipboard]::GetImage()
		if ($null -eq $img) { 'no image'; return }
		$img.Save('%s', [System.Drawing.Imaging.ImageFormat]::Png)
		'ok'
	`, tmpFile)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read clipboard: %w", err)
	}

	if strings.TrimSpace(string(output)) == "no image" {
		return nil, nil
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read clipboard image: %w", err)
	}
	return newClipboardImage(data)
}

// ReadClipboard reads an image from the clipboard as a core.Image.
// Returns nil, nil if no image is available (not an error).
func ReadClipboard() (*core.Image, error) {
	switch runtime.GOOS {
	case "darwin":
		return readClipboardMacOS()
	case "linux":
		return readClipboardLinux()
	case "windows":
		return readClipboardWindows()
	default:
		return nil, fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
}
