package media

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
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
			result, err := Process(original, tt.mimeType, tt.maxWidth, tt.compress)
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
		result, err := Process(nil, "image/png", IconMaxWidth, compress)
		if err == nil {
			t.Fatalf("Process(compress=%v) error = nil, want an empty-image error", compress)
		}
		if len(result.Data) != 0 || result.Hash != "" {
			t.Fatalf("Process(compress=%v) returned a result for empty input: %+v", compress, result)
		}
		if !strings.Contains(err.Error(), "detecting image format") {
			t.Fatalf("Process(compress=%v) error = %q, want it classified as an unreadable image", compress, err)
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
	original, err := Process(encodePNGTestImage(64, 32), "image/png", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Process(original.Data, "image/webp", IconMaxWidth, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.MimeType != "image/webp" || !bytes.Equal(again.Data, original.Data) {
		t.Fatalf("in-limit WebP changed: mime %s equal %v", again.MimeType, bytes.Equal(again.Data, original.Data))
	}
}

func TestProcessResizesWideWebP(t *testing.T) {
	original, err := Process(encodePNGTestImage(1024, 512), "image/png", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	resized, err := Process(original.Data, "image/webp", IconMaxWidth, true)
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
	result, _ := Process(data, "image/png", 0, false)
	return result.Hash
}
