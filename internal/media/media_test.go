package media

import (
	"bytes"
	"image"
	"image/color"
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
			name: "large PNG icon is resized",
			encode: func() []byte {
				return encodePNGTestImage(1024, 512)
			},
			mimeType:  "image/png",
			maxWidth:  IconMaxWidth,
			compress:  true,
			wantMIME:  "image/png",
			wantWidth: 512,
		},
		{
			name: "large JPEG screenshot is resized",
			encode: func() []byte {
				return encodeJPEGTestImage(2880, 1440)
			},
			mimeType:  "image/jpeg",
			maxWidth:  ScreenshotMaxWidth,
			compress:  true,
			wantMIME:  "image/jpeg",
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
			name:     "WebP preserves format",
			encode:   func() []byte { return []byte("RIFF....WEBP") },
			mimeType: "image/webp",
			maxWidth: ScreenshotMaxWidth,
			compress: true,
			wantMIME: "image/webp",
			wantSame: true,
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

func hashBytes(data []byte) string {
	result, _ := Process(data, "image/png", 0, false)
	return result.Hash
}
