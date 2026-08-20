package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var (
	githubLatestRelease = "https://github.com/genai-io/san/releases/latest"
	httpClient          = http.DefaultClient
)

// releaseInfo represents the GitHub API response for a release.
type releaseInfo struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func runSelfUpdate(ctx context.Context) error {
	currentVersion := strings.TrimPrefix(version, "v")

	fmt.Printf("Current version: v%s\n", currentVersion)

	// Fetch latest release info
	latest, err := fetchLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	latestVersion := strings.TrimPrefix(latest.TagName, "v")
	fmt.Printf("Latest version:  v%s\n", latestVersion)

	// Compare versions
	if latestVersion == currentVersion {
		fmt.Println("Already up to date.")
		return nil
	}

	fmt.Printf("New version available: v%s -> v%s\n", currentVersion, latestVersion)

	if !confirm("Download and install?") {
		fmt.Println("Update cancelled.")
		return nil
	}

	// Find the right asset
	archiveExt := ".tar.gz"
	if runtime.GOOS == "windows" {
		archiveExt = ".zip"
	}
	assetName := fmt.Sprintf("san_%s_%s%s", runtime.GOOS, goArch(runtime.GOARCH), archiveExt)
	downloadURL := fmt.Sprintf("https://github.com/genai-io/san/releases/download/v%s/%s", latestVersion, assetName)

	// Find current binary path
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine binary path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("cannot resolve binary path: %w", err)
	}

	// Download and extract to a temp directory on the same filesystem as
	// the target binary so the final rename stays within one filesystem
	// and doesn't hit EXDEV (cross-device link).
	fmt.Printf("Downloading %s ...\n", assetName)
	tmpDir, err := os.MkdirTemp(filepath.Dir(exe), ".san-update-*")
	if err != nil {
		return fmt.Errorf("cannot create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archive := filepath.Join(tmpDir, assetName)
	if err := downloadWithProgress(ctx, downloadURL, archive); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Extract the archive
	if runtime.GOOS == "windows" {
		if err := extractZip(archive, tmpDir); err != nil {
			return fmt.Errorf("extract failed: %w", err)
		}
	} else {
		if err := extractTarGz(archive, tmpDir); err != nil {
			return fmt.Errorf("extract failed: %w", err)
		}
	}

	// Determine binary name (san.exe on Windows, san on other platforms)
	binName := "san"
	if runtime.GOOS == "windows" {
		binName = "san.exe"
	}
	binPath := filepath.Join(tmpDir, binName)

	// Verify the extracted binary exists
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("extracted binary not found: %w", err)
	}

	newFile, err := os.Stat(binPath)
	if err != nil {
		return fmt.Errorf("cannot stat new binary: %w", err)
	}

	if err := os.Chmod(binPath, newFile.Mode()); err != nil {
		return fmt.Errorf("cannot chmod new binary: %w", err)
	}

	// Replace the current binary
	backupPath := exe + ".bak"
	if err := os.Rename(exe, backupPath); err != nil {
		return fmt.Errorf("cannot backup current binary: %w", err)
	}

	if err := os.Rename(binPath, exe); err != nil {
		// On cross-device link (EXDEV), fall back to copy+delete
		// instead of aborting — some setups have /tmp on a different
		// filesystem than the target binary directory.
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
			if err := copyFile(exe, binPath); err != nil {
				_ = os.Rename(backupPath, exe)
				return fmt.Errorf("cannot install update (copy fallback failed): %w", err)
			}
			_ = os.Remove(binPath)
		} else {
			// Restore backup
			_ = os.Rename(backupPath, exe)
			return fmt.Errorf("cannot install update: %w", err)
		}
	}
	// Remove the backup. On Windows, the running process still has a handle
	// to the renamed file, so this will fail silently. The .bak file is
	// cleaned up on the next startup.
	_ = os.Remove(backupPath)

	fmt.Printf("Updated to v%s\n", latestVersion)
	fmt.Printf("Installed to: %s\n", exe)
	fmt.Println("Restart san to use the new version.")
	return nil
}

// fetchLatestRelease fetches the latest release tag from GitHub
// without hitting the rate-limited API.
func fetchLatestRelease(ctx context.Context) (*releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, githubLatestRelease, nil)
	if err != nil {
		return nil, err
	}

	// Don't follow redirects — we need the Location header
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("unexpected response from GitHub: %s", resp.Status)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return nil, fmt.Errorf("missing Location header in redirect response")
	}

	// Location: https://github.com/genai-io/san/releases/tag/v1.21.4
	version := strings.TrimPrefix(location, "https://github.com/genai-io/san/releases/tag/v")
	if version == location {
		return nil, fmt.Errorf("unexpected Location header: %s", location)
	}

	return &releaseInfo{TagName: "v" + version}, nil
}

// downloadWithProgress downloads a file from url to dest with a terminal progress bar.
func downloadWithProgress(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	total := resp.ContentLength
	if total > 0 {
		pw := &progressWriter{
			w:     out,
			total: total,
			done:  make(chan struct{}),
		}
		go pw.spin()
		_, err = io.Copy(pw, resp.Body)
		close(pw.done)
		fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", 60)+"\r")
	} else {
		_, err = io.Copy(out, resp.Body)
	}
	return err
}

// progressWriter wraps an io.Writer and prints a progress bar on stderr.
type progressWriter struct {
	w       io.Writer
	total   int64
	written int64
	done    chan struct{}
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.written += int64(n)
	return n, err
}

func (pw *progressWriter) spin() {
	const barWidth = 30
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-pw.done:
			pw.printBar(barWidth)
			return
		case <-ticker.C:
			pw.printBar(barWidth)
		}
	}
}

func (pw *progressWriter) printBar(width int) {
	pct := float64(pw.written) / float64(pw.total)
	filled := min(int(pct*float64(width)), width)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	fmt.Fprintf(os.Stderr, "\r  downloading [%s] %3.0f%%", bar, pct*100)
}

// extractTarGz extracts a tar.gz archive to destDir.
func extractTarGz(tarball, destDir string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)

		// Prevent tar slip attacks
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			_ = out.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// extractZip extracts a zip archive to destDir.
func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)

		// Prevent zip slip attacks
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in zip: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies src to dst (permissions preserved).
// Used as a fallback when os.Rename fails with EXDEV.
func copyFile(dst, src string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Preserve source file permissions
	fi, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Close()
}

// goArch maps Go arch names to release asset arch names.
func goArch(arch string) string {
	switch arch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return arch
	}
}

// confirm prompts the user for a yes/no answer and returns true for "yes".
func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}
