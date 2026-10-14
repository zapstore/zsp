package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestProcess(t *testing.T) {
	tests := []struct {
		name      string
		encode    func() []byte
		mimeType  string
		maxWidth  int
		compress  bool
		wantMIME  string
		wantWidth int
		wantSame  bool
	}{
		{
			name: "large PNG icon is resized to WebP",
			encode: func() []byte {
				return encodePNGTestImage(1024, 512)
			},
			mimeType:  "image/png",
			maxWidth:  IconMaxWidth,
			compress:  true,
			wantMIME:  "image/webp",
			wantWidth: 512,
		},
		{
			name: "PNG within the width cap becomes WebP",
			encode: func() []byte {
				return encodePNGTestImage(64, 64)
			},
			mimeType:  "image/png",
			maxWidth:  IconMaxWidth,
			compress:  true,
			wantMIME:  "image/webp",
			wantWidth: 64,
		},
		{
			name: "large JPEG screenshot is resized to WebP",
			encode: func() []byte {
				return encodeJPEGTestImage(2880, 1440)
			},
			mimeType:  "image/jpeg",
			maxWidth:  ScreenshotMaxWidth,
			compress:  true,
			wantMIME:  "image/webp",
			wantWidth: 1440,
		},
		{
			name: "no compress labels the detected type",
			encode: func() []byte {
				return encodeJPEGTestImage(64, 64)
			},
			mimeType: "image/png",
			maxWidth: IconMaxWidth,
			compress: false,
			wantMIME: "image/jpeg",
			wantSame: true,
		},
		{
			name: "no compress preserves bytes",
			encode: func() []byte {
				return encodePNGTestImage(1024, 512)
			},
			mimeType: "image/png",
			maxWidth: IconMaxWidth,
			compress: false,
			wantMIME: "image/png",
			wantSame: true,
		},
		{
			name: "GIF becomes WebP",
			encode: func() []byte {
				return encodeGIFTestImage(16, 16)
			},
			mimeType:  "image/gif",
			maxWidth:  ScreenshotMaxWidth,
			compress:  true,
			wantMIME:  "image/webp",
			wantWidth: 16,
		},
		{
			name: "wide GIF is resized to WebP",
			encode: func() []byte {
				return encodeGIFTestImage(1024, 512)
			},
			mimeType:  "image/gif",
			maxWidth:  IconMaxWidth,
			compress:  true,
			wantMIME:  "image/webp",
			wantWidth: 512,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := tt.encode()
			result, err := Process(original, tt.maxWidth, tt.compress)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if result.MimeType != tt.wantMIME {
				t.Fatalf("MIME type = %q, want %q", result.MimeType, tt.wantMIME)
			}
			if tt.wantSame {
				if !bytes.Equal(result.Data, original) {
					t.Fatal("no-compress changed image bytes")
				}
				return
			}
			config, _, err := image.DecodeConfig(bytes.NewReader(result.Data))
			if err != nil {
				t.Fatalf("decoded result: %v", err)
			}
			if config.Width != tt.wantWidth {
				t.Fatalf("width = %d, want %d", config.Width, tt.wantWidth)
			}
			if result.Hash == "" || result.Hash == hashBytes(original) {
				t.Fatal("compressed result did not receive a new content hash")
			}
		})
	}
}

func TestProcessRejectsEmptyImage(t *testing.T) {
	for _, compress := range []bool{true, false} {
		result, err := Process(nil, IconMaxWidth, compress)
		if err == nil {
			t.Fatalf("Process(compress=%v) error = nil, want an empty-image error", compress)
		}
		if len(result.Data) != 0 || result.Hash != "" {
			t.Fatalf("Process(compress=%v) returned a result for empty input: %+v", compress, result)
		}
		if !errors.Is(err, ErrInvalidImage) {
			t.Fatalf("Process(compress=%v) error = %q, want ErrInvalidImage", compress, err)
		}
	}
}

func encodePNGTestImage(width, height int) []byte {
	var buf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 255), G: uint8(y % 255), A: 255})
		}
	}
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func encodeJPEGTestImage(width, height int) []byte {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 100, A: 255})
		}
	}
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100})
	return buf.Bytes()
}

func TestProcessKeepsWebPWithinTheWidthCap(t *testing.T) {
	original, err := Process(encodePNGTestImage(64, 32), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Process(original.Data, IconMaxWidth, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.MimeType != "image/webp" || !bytes.Equal(again.Data, original.Data) {
		t.Fatalf("in-limit WebP changed: mime %s equal %v", again.MimeType, bytes.Equal(again.Data, original.Data))
	}
}

func TestProcessResizesWideWebP(t *testing.T) {
	original, err := Process(encodePNGTestImage(1024, 512), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	resized, err := Process(original.Data, IconMaxWidth, true)
	if err != nil {
		t.Fatal(err)
	}
	if resized.MimeType != "image/webp" {
		t.Fatalf("MIME type = %q", resized.MimeType)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(resized.Data))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != IconMaxWidth {
		t.Fatalf("width = %d, want %d", config.Width, IconMaxWidth)
	}
}

func encodeGIFTestImage(width, height int) []byte {
	var buf bytes.Buffer
	img := image.NewPaletted(image.Rect(0, 0, width, height), color.Palette{color.Black, color.White})
	_ = gif.Encode(&buf, img, nil)
	return buf.Bytes()
}

func hashBytes(data []byte) string {
	result, _ := Process(data, 0, false)
	return result.Hash
}

func TestProcessRejectsUndetectableImages(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"html", []byte("<html><body>not an image</body></html>")},
		{"plain text", []byte("not an image")},
		{"truncated PNG header", encodePNGTestImage(8, 8)[:12]},
		{"xml without svg root", []byte(`<?xml version="1.0"?><note>hi</note>`)},
	}
	for _, tt := range tests {
		for _, compress := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s compress=%v", tt.name, compress), func(t *testing.T) {
				result, err := Process(tt.data, IconMaxWidth, compress)
				if !errors.Is(err, ErrInvalidImage) {
					t.Fatalf("Process(%q) error = %v, want ErrInvalidImage", tt.name, err)
				}
				if len(result.Data) != 0 {
					t.Fatalf("Process(%q) returned data for rejected input", tt.name)
				}
			})
		}
	}
}

func TestProcessRejectsOversizedDimensions(t *testing.T) {
	var buf bytes.Buffer
	// A PNG declaring 7000x7000 pixels costs little to encode
	// but is over the decoding limit.
	img := image.NewGray(image.Rect(0, 0, 7000, 7000))
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	for _, compress := range []bool{true, false} {
		if _, err := Process(buf.Bytes(), IconMaxWidth, compress); !errors.Is(err, ErrInvalidImage) {
			t.Fatalf("Process(7000x7000 PNG, compress=%v) error = %v, want ErrInvalidImage", compress, err)
		}
	}
}

func TestProcessSVG(t *testing.T) {
	tests := []struct {
		name    string
		svg     string
		wantErr bool
	}{
		{"plain", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`, false},
		{"xml prolog and comment", `<?xml version="1.0"?><!-- logo --><svg xmlns="http://www.w3.org/2000/svg"><title>javascript tips</title></svg>`, false},
		{"no namespace", `<svg><path d="M0 0"/></svg>`, false},
		{"script element", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, true},
		{"uppercase script element", `<svg xmlns="http://www.w3.org/2000/svg"><SCRIPT>alert(1)</SCRIPT></svg>`, true},
		{"prefixed script element", `<s:svg xmlns:s="http://www.w3.org/2000/svg"><s:script>alert(1)</s:script></s:svg>`, true},
		{"event handler", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`, true},
		{"nested event handler", `<svg xmlns="http://www.w3.org/2000/svg"><rect onClick="alert(1)"/></svg>`, true},
		{"foreignObject", `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div/></foreignObject></svg>`, true},
		{"javascript href", `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><a xlink:href="javascript:alert(1)"><rect/></a></svg>`, true},
		{"obfuscated javascript href", `<svg xmlns="http://www.w3.org/2000/svg"><a href=" JaVa&#9;Script:alert(1)"><rect/></a></svg>`, true},
		{"entity declaration", `<!DOCTYPE svg [<!ENTITY a "aaaa">]><svg xmlns="http://www.w3.org/2000/svg"/>`, true},
		{"wrong root", `<html xmlns="http://www.w3.org/1999/xhtml"/>`, true},
		{"svg root in another namespace", `<svg xmlns="http://example.com/other"/>`, true},
		{"unterminated", `<svg xmlns="http://www.w3.org/2000/svg"><rect>`, true},
	}
	for _, tt := range tests {
		for _, compress := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s compress=%v", tt.name, compress), func(t *testing.T) {
				result, err := Process([]byte(tt.svg), IconMaxWidth, compress)
				if tt.wantErr {
					if !errors.Is(err, ErrInvalidImage) {
						t.Fatalf("Process(%s) error = %v, want ErrInvalidImage", tt.name, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("Process(%s) error = %v", tt.name, err)
				}
				if result.MimeType != "image/svg+xml" || string(result.Data) != tt.svg || result.Changed {
					t.Fatalf("Process(%s) = mime %q changed %v, want unchanged image/svg+xml", tt.name, result.MimeType, result.Changed)
				}
			})
		}
	}
}

func TestDecodeBounded(t *testing.T) {
	img, format, err := DecodeBounded(encodePNGTestImage(4, 3))
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" || img.Bounds().Dx() != 4 || img.Bounds().Dy() != 3 {
		t.Fatalf("DecodeBounded() = %s %v, want 4x3 png", format, img.Bounds())
	}
	if _, _, err := DecodeBounded([]byte("junk")); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("DecodeBounded(junk) error = %v, want ErrInvalidImage", err)
	}
}
