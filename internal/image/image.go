// Package image provides image loading, validation, and encoding utilities.
package image

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/genai-io/san/internal/core"
)

const (
	// maxImageSize is the maximum allowed image size (5MB)
	maxImageSize = 5 * 1024 * 1024
)

// supportedTypes maps file extensions to MIME types
var supportedTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// Load reads, validates, and base64-encodes an image from the given path.
func Load(path string) (core.Image, error) {
	// Resolve path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return core.Image{}, fmt.Errorf("invalid path: %w", err)
	}

	// Check if file exists
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return core.Image{}, fmt.Errorf("file not found: %s", path)
		}
		return core.Image{}, fmt.Errorf("cannot access file: %w", err)
	}

	// Check file size
	if info.Size() > maxImageSize {
		return core.Image{}, fmt.Errorf("image too large: %d bytes (max %d)", info.Size(), maxImageSize)
	}

	// Check extension
	ext := strings.ToLower(filepath.Ext(absPath))
	mediaType, ok := supportedTypes[ext]
	if !ok {
		return core.Image{}, fmt.Errorf("unsupported image format: %s", ext)
	}

	// Read file
	data, err := os.ReadFile(absPath)
	if err != nil {
		return core.Image{}, fmt.Errorf("failed to read file: %w", err)
	}

	// Detect actual content type to verify
	detectedType := http.DetectContentType(data)
	if !strings.HasPrefix(detectedType, "image/") {
		return core.Image{}, fmt.Errorf("file is not a valid image")
	}

	return newImage(mediaType, filepath.Base(absPath), absPath, data), nil
}

// newImage builds a core.Image from raw bytes, base64-encoding the data.
func newImage(mediaType, fileName, path string, data []byte) core.Image {
	return core.Image{
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(data),
		FileName:  fileName,
		Path:      path,
		Size:      len(data),
	}
}

// EnsureFilePath returns a filesystem path for an image, writing the file when
// the image doesn't have one — so a tool handed the path (an MCP image
// describer, say) can always open it. An image loaded from disk keeps its own
// path and temp is false. A clipboard paste has no backing file, so its bytes
// go to a temp file and temp is true: that one belongs to the caller, who must
// os.Remove it once the model is done with it.
func EnsureFilePath(img core.Image) (path string, temp bool, err error) {
	if img.Path != "" {
		return img.Path, false, nil
	}
	if img.Data == "" {
		return "", false, fmt.Errorf("image %s has neither a path nor data", img.FileName)
	}
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return "", false, fmt.Errorf("decoding image data: %w", err)
	}
	f, err := os.CreateTemp("", "san-image-*"+extForMediaType(img.MediaType))
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", false, err
	}
	return f.Name(), true, nil
}

// extForMediaType picks the file extension a temp image should carry. It does
// not reverse supportedTypes: two extensions map to image/jpeg there, and Go
// randomises map iteration, so the same image would land on .jpg or .jpeg from
// run to run.
func extForMediaType(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}
