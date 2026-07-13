package feishu

import (
	"bytes"
	"os"
	"testing"
)

func TestIsFeishuMessageImageRecognizesSupportedImageSignatures(t *testing.T) {
	tests := []struct {
		name     string
		contents []byte
		want     bool
	}{
		{name: "png", contents: append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("payload")...), want: true},
		{name: "jpeg", contents: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}, want: true},
		{name: "gif", contents: []byte("GIF89a"), want: true},
		{name: "webp", contents: []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), want: true},
		{name: "ordinary file", contents: []byte("not an image"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "payload-*")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Write(tc.contents); err != nil {
				t.Fatal(err)
			}
			if _, err := file.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			got, err := isFeishuMessageImage(file)
			if err != nil || got != tc.want {
				t.Fatalf("isFeishuMessageImage() = %v, %v; want %v", got, err, tc.want)
			}
			contents, err := os.ReadFile(file.Name())
			if err != nil || !bytes.Equal(contents, tc.contents) {
				t.Fatalf("file changed after inspection: contents=%x err=%v", contents, err)
			}
			position, err := file.Seek(0, 1)
			if err != nil || position != 0 {
				t.Fatalf("file offset after inspection = %d, %v; want 0", position, err)
			}
		})
	}
}
