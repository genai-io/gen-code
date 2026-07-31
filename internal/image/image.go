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

// ResolvePath returns a filesystem path for an image. Images loaded from a
// file keep their original path; images without a backing file (e.g.
// clipboard pastes) are materialized to a temporary file so tools such as MCP
// image describers can read them.
func ResolvePath(img core.Image) (string, error) {
	if img.Path != "" {
		return img.Path, nil
	}
	if img.Data == "" {
		return img.FileName, nil
	}
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return "", fmt.Errorf("decoding image data: %w", err)
	}
	f, err := os.CreateTemp("", "san-image-*"+extForMediaType(img.MediaType))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func extForMediaType(mediaType string) string {
	for ext, mt := range supportedTypes {
		if mt == mediaType {
			return ext
		}
	}
	return ".png"
}
