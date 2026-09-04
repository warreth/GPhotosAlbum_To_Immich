package googlephotos

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestDownloadMediaDoesNotProbeVideoForConfirmedImage(t *testing.T) {
	videoProbeCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.RawQuery, "=dv") || strings.HasSuffix(r.URL.String(), "=dv") {
			videoProbeCount++
			http.Error(w, "unexpected video probe", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0})
	}))
	defer server.Close()

	client := &Client{
		client: server.Client(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	_, ext, isVideo, isLivePhoto, err := DownloadMedia(context.Background(), client, server.URL+"/media?")
	if err != nil {
		t.Fatalf("DownloadMedia returned an error: %v", err)
	}
	if videoProbeCount != 0 {
		t.Fatalf("expected no =dv request for a confirmed JPEG, got %d", videoProbeCount)
	}
	if ext != ".jpg" || isVideo || isLivePhoto {
		t.Fatalf("unexpected classification: ext=%q isVideo=%v isLivePhoto=%v", ext, isVideo, isLivePhoto)
	}
}
