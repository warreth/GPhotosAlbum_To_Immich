package googlephotos

import "testing"

func TestIsLikelyLivePhotoSidecar(t *testing.T) {
	marker := []byte("com.apple.quicktime.content.identifier")

	t.Run("recognizes normal live photo", func(t *testing.T) {
		data := append([]byte("video-data-"), marker...)
		if !isLikelyLivePhotoSidecar(data) {
			t.Fatal("expected Apple metadata marker to identify a Live Photo")
		}
	})

	t.Run("recognizes live photo larger than 25 MiB", func(t *testing.T) {
		data := make([]byte, 26*1024*1024)
		copy(data[len(data)-len(marker):], marker)
		if !isLikelyLivePhotoSidecar(data) {
			t.Fatal("expected file size not to override Apple metadata")
		}
	})

	t.Run("does not classify ordinary video as live photo", func(t *testing.T) {
		data := []byte{
			0x00, 0x00, 0x00, 0x18,
			'f', 't', 'y', 'p',
			'i', 's', 'o', 'm',
		}
		if isLikelyLivePhotoSidecar(data) {
			t.Fatal("ordinary video without Apple Live Photo metadata was misclassified")
		}
	})
}
