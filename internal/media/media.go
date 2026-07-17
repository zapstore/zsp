// Package media prepares image assets before they are hashed and uploaded.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	_ "image/gif"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	IconMaxWidth       = 512
	ScreenshotMaxWidth = 1440
	jpegQuality        = 88
)

// Result contains the final bytes and metadata for an image asset.
type Result struct {
	Data         []byte
	MimeType     string
	Hash         string
	OriginalSize int
	Changed      bool
}

// Process decodes and optimizes supported raster formats without converting
// them to another format. Unsupported formats are returned unchanged.
func Process(data []byte, mimeType string, maxWidth int, compress bool) (Result, error) {
	result := Result{
		Data:         data,
		MimeType:     normalizeMimeType(mimeType),
		OriginalSize: len(data),
	}
	if !compress || len(data) == 0 {
		return withHash(result), nil
	}
	if result.MimeType == "image/webp" || result.MimeType == "image/gif" || result.MimeType == "image/svg+xml" {
		return withHash(result), nil
	}

	format, err := detectFormat(data)
	if err != nil {
		return Result{}, fmt.Errorf("detecting image format: %w", err)
	}
	result.MimeType = format.mimeType
	if format.mimeType != "image/png" && format.mimeType != "image/jpeg" {
		return withHash(result), nil
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("decoding %s image: %w", format.name, err)
	}

	var dst image.Image = src
	width := src.Bounds().Dx()
	if maxWidth > 0 && width > maxWidth {
		height := src.Bounds().Dy() * maxWidth / width
		if height < 1 {
			height = 1
		}
		resized := image.NewRGBA(image.Rect(0, 0, maxWidth, height))
		draw.CatmullRom.Scale(resized, resized.Bounds(), src, src.Bounds(), draw.Over, nil)
		dst = resized
	}

	var output bytes.Buffer
	switch format.mimeType {
	case "image/png":
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		if err := encoder.Encode(&output, dst); err != nil {
			return Result{}, fmt.Errorf("encoding PNG image: %w", err)
		}
	case "image/jpeg":
		if err := jpeg.Encode(&output, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return Result{}, fmt.Errorf("encoding JPEG image: %w", err)
		}
	}

	result.Data = output.Bytes()
	result.Changed = !bytes.Equal(data, result.Data)
	return withHash(result), nil
}

func withHash(result Result) Result {
	hash := sha256.Sum256(result.Data)
	result.Hash = hex.EncodeToString(hash[:])
	return result
}

type imageFormat struct {
	name     string
	mimeType string
}

func detectFormat(data []byte) (imageFormat, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return imageFormat{}, err
	}
	switch format {
	case "png":
		return imageFormat{name: "PNG", mimeType: "image/png"}, nil
	case "jpeg":
		return imageFormat{name: "JPEG", mimeType: "image/jpeg"}, nil
	case "webp":
		return imageFormat{name: "WebP", mimeType: "image/webp"}, nil
	case "gif":
		return imageFormat{name: "GIF", mimeType: "image/gif"}, nil
	default:
		return imageFormat{name: format, mimeType: normalizeMimeType("")}, nil
	}
}

func normalizeMimeType(mimeType string) string {
	mimeType = strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "image/svg+xml":
		return mimeType
	default:
		return "application/octet-stream"
	}
}

// Keep the WebP decoder linked so DecodeConfig recognizes WebP input. There
// is intentionally no WebP encoder: preserving the source format is required.
var _ = webp.DecodeConfig
