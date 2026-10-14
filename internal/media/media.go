// Package media prepares image assets before they are hashed and uploaded.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
	"unicode"

	"github.com/chai2010/webp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	IconMaxWidth       = 512
	ScreenshotMaxWidth = 1440
	webpQuality        = 80
	maxDecodedPixels   = 40_000_000
	svgNamespace       = "http://www.w3.org/2000/svg"
)

// ErrInvalidImage marks input that is not a usable image: unreadable,
// unsupported, over the decoding limits, or an unsafe SVG.
var ErrInvalidImage = errors.New("invalid image")

var mimeTypes = map[string]string{
	"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
}

var errNotSVG = fmt.Errorf("%w: not an SVG document", ErrInvalidImage)

// Result contains the final bytes and metadata for an image asset.
type Result struct {
	Data         []byte
	MimeType     string
	Hash         string
	OriginalSize int
	Changed      bool
}

// Process resizes PNG, JPEG, GIF, and WebP that exceed maxWidth and encodes
// PNG, JPEG, and GIF as WebP. A GIF contributes its first frame. WebP already
// within maxWidth is returned unchanged. SVG is returned unchanged when it
// parses as SVG and carries no script, event handler, foreignObject, or
// javascript: URL. compress false returns the original bytes.
//
// The format is always detected from the content and sets Result.MimeType.
// Undetectable formats, images over the decoding limits, and unsafe SVG fail
// with an error matching ErrInvalidImage.
func Process(data []byte, maxWidth int, compress bool) (Result, error) {
	// Empty input is not a usable image. Rejecting it here keeps a zero-byte
	// blob from being prepared and later rejected by the Blossom server for a
	// zero X-Content-Length.
	if len(data) == 0 {
		return Result{}, fmt.Errorf("%w: empty image data", ErrInvalidImage)
	}
	result := Result{Data: data, OriginalSize: len(data)}

	format, err := detectFormat(data)
	if err != nil {
		svgErr := validateSVG(data)
		switch {
		case svgErr == nil:
			result.MimeType = "image/svg+xml"
			return withHash(result), nil
		case errors.Is(svgErr, errNotSVG):
			return Result{}, err
		default:
			return Result{}, svgErr
		}
	}
	result.MimeType = format.mimeType
	if !compress {
		return withHash(result), nil
	}

	needsResize := maxWidth > 0 && format.width > maxWidth
	if format.mimeType == "image/webp" && !needsResize {
		return withHash(result), nil
	}

	src, _, err := DecodeBounded(data)
	if err != nil {
		return Result{}, err
	}

	dst := src
	if needsResize {
		height := src.Bounds().Dy() * maxWidth / src.Bounds().Dx()
		if height < 1 {
			height = 1
		}
		resized := image.NewNRGBA(image.Rect(0, 0, maxWidth, height))
		draw.CatmullRom.Scale(resized, resized.Bounds(), src, src.Bounds(), draw.Over, nil)
		dst = resized
	}

	encoded, err := encodeWebP(dst)
	if err != nil {
		return Result{}, err
	}
	result.Data = encoded
	result.MimeType = "image/webp"
	result.Changed = !bytes.Equal(data, result.Data)
	return withHash(result), nil
}

// ConfigBounded reads the image header and returns its configuration and
// format name ("png", "jpeg", "gif", or "webp"). It fails with ErrInvalidImage
// for other formats and for dimensions beyond the decoding limit, without
// decoding any pixels.
func ConfigBounded(data []byte) (image.Config, string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, "", fmt.Errorf("%w: detect format: %w", ErrInvalidImage, err)
	}
	if _, ok := mimeTypes[format]; !ok {
		return image.Config{}, "", fmt.Errorf("%w: unsupported format %q", ErrInvalidImage, format)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxDecodedPixels {
		return image.Config{}, "", fmt.Errorf("%w: dimensions %dx%d exceed processing limit", ErrInvalidImage, config.Width, config.Height)
	}
	return config, format, nil
}

// DecodeBounded decodes a PNG, JPEG, GIF, or WebP image after checking its
// declared dimensions with ConfigBounded. It returns the format name.
func DecodeBounded(data []byte) (image.Image, string, error) {
	if _, _, err := ConfigBounded(data); err != nil {
		return nil, "", err
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: decode image: %w", ErrInvalidImage, err)
	}
	return img, format, nil
}

func encodeWebP(img image.Image) ([]byte, error) {
	var output bytes.Buffer
	// Exact keeps RGB under transparent pixels so icons do not grow a halo.
	err := webp.Encode(&output, img, &webp.Options{Quality: webpQuality, Exact: true})
	if err != nil {
		return nil, fmt.Errorf("%w: encode WebP: %w", ErrInvalidImage, err)
	}
	return output.Bytes(), nil
}

func withHash(result Result) Result {
	hash := sha256.Sum256(result.Data)
	result.Hash = hex.EncodeToString(hash[:])
	return result
}

type imageFormat struct {
	mimeType string
	width    int
	height   int
}

func detectFormat(data []byte) (imageFormat, error) {
	config, format, err := ConfigBounded(data)
	if err != nil {
		return imageFormat{}, err
	}
	return imageFormat{mimeType: mimeTypes[format], width: config.Width, height: config.Height}, nil
}

// validateSVG accepts data only when it is well-formed XML with an svg root
// and nothing that runs code when a browser renders the file. A document that
// is not SVG at all fails with errNotSVG.
func validateSVG(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	seenRoot := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if !seenRoot {
				return fmt.Errorf("%w: %w", errNotSVG, err)
			}
			return fmt.Errorf("%w: parse SVG: %w", ErrInvalidImage, err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if !seenRoot {
				if token.Name.Local != "svg" || (token.Name.Space != "" && token.Name.Space != svgNamespace) {
					return errNotSVG
				}
				seenRoot = true
			}
			if err := checkSVGElement(token); err != nil {
				return err
			}
		case xml.Directive:
			if bytes.Contains(bytes.ToUpper(token), []byte("<!ENTITY")) {
				return fmt.Errorf("%w: SVG declares an entity", ErrInvalidImage)
			}
		}
	}
	if !seenRoot {
		return errNotSVG
	}
	return nil
}

func checkSVGElement(element xml.StartElement) error {
	switch strings.ToLower(element.Name.Local) {
	case "script", "foreignobject":
		return fmt.Errorf("%w: SVG contains <%s>", ErrInvalidImage, element.Name.Local)
	}
	for _, attr := range element.Attr {
		if strings.HasPrefix(strings.ToLower(attr.Name.Local), "on") {
			return fmt.Errorf("%w: SVG contains event handler %q", ErrInvalidImage, attr.Name.Local)
		}
		if containsJavaScriptURL(attr.Value) {
			return fmt.Errorf("%w: SVG contains a javascript: URL", ErrInvalidImage)
		}
	}
	return nil
}

// containsJavaScriptURL ignores whitespace and control characters because
// browsers strip them from URL schemes.
func containsJavaScriptURL(value string) bool {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
	return strings.Contains(stripped, "javascript:")
}
