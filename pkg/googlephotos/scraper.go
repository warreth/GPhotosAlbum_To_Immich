package googlephotos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Album struct {
	ID     string
	Title  string
	Photos []Photo
}

type Photo struct {
	ID          string
	URL         string
	Width       int
	Height      int
	TakenAt     time.Time
	Description string
	Uploader    string
	IsVideo     bool
}

// ScrapeAlbum parses a Google Photos shared album URL and returns the Album structure.
func ScrapeAlbum(client *Client, url string) (*Album, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to fetch album: %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	htmlContent := string(bodyBytes)

	// Extract Title from OG:TITLE
	title := "Google Photos Album"
	titleRe := regexp.MustCompile(`<meta property="og:title" content="([^"]+)">`)
	titleMatch := titleRe.FindStringSubmatch(htmlContent)
	if len(titleMatch) > 1 {
		title = titleMatch[1]
	}

	// Clean Title
	title = html.UnescapeString(title)
	// Remove Date Range Suffix (e.g. " · Feb 6–7") and emojis
	dateSuffixRe := regexp.MustCompile(`\s*·.*$`)
	title = dateSuffixRe.ReplaceAllString(title, "")
	title = strings.TrimSpace(title)
	title = strings.TrimSuffix(title, " 📸")

	// Find the start of the data
	// Look for key: 'ds:1' followed by data:
	startRe := regexp.MustCompile(`key:\s*'ds:1'.*?data:`)
	loc := startRe.FindStringIndex(htmlContent)
	if loc == nil {
		return nil, fmt.Errorf("could not find album data (ds:1) in page")
	}

	startPos := loc[1]
	// Scan forward for first '['
	jsonStart := -1
	for i := startPos; i < len(htmlContent); i++ {
		if htmlContent[i] == '[' {
			jsonStart = i
			break
		}
	}
	if jsonStart == -1 {
		return nil, fmt.Errorf("could not find start of JSON array")
	}

	// Balance brackets to find the end of the JSON array
	balance := 0
	inString := false
	escape := false
	jsonEnd := -1

	for i := jsonStart; i < len(htmlContent); i++ {
		char := htmlContent[i]

		if escape {
			escape = false
			continue
		}

		if char == '\\' {
			escape = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if !inString {
			if char == '[' {
				balance++
			} else if char == ']' {
				balance--
				if balance == 0 {
					jsonEnd = i + 1
					break
				}
			}
		}
	}

	if jsonEnd == -1 {
		return nil, fmt.Errorf("could not find end of JSON array")
	}

	jsonStr := htmlContent[jsonStart:jsonEnd]
	
	// Pre-cleanup of JSON string if needed (sometimes unescaping)
	// Usually it's valid JSON directly in the script tag
	
	var data []interface{}
	err = json.Unmarshal([]byte(jsonStr), &data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse album JSON: %v", err)
	}

	// Structure: [metadata, [item1, item2, ...], token, ...]
	// Index 1 is usually the item list.
	var list []interface{}
	if len(data) > 1 {
		if l, ok := data[1].([]interface{}); ok {
			list = l
		}
	}
	// Fallback check
	if list == nil && len(data) > 0 {
		if l, ok := data[0].([]interface{}); ok {
			list = l
		}
	}

	var photos []Photo

	extractInt := func(v interface{}) (int64, bool) {
		switch val := v.(type) {
		case string:
			if i, err := strconv.ParseInt(val, 10, 64); err == nil {
				return i, true
			}
		case float64:
			return int64(val), true
		}
		return 0, false
	}

	for _, item := range list {
		// Each item is an array
		// [ID, [URL, w, h], [Timestamp_ms, ...], ...]
		itemArr, ok := item.([]interface{})
		if !ok || len(itemArr) < 2 {
			continue
		}
		
		id, _ := itemArr[0].(string)
		
		// Media Info
		mediaArr, ok := itemArr[1].([]interface{})
		if !ok || len(mediaArr) < 1 {
			continue
		}
		
		url, _ := mediaArr[0].(string)
		w := 0
		h := 0
		if len(mediaArr) >= 3 {
			if fw, ok := mediaArr[1].(float64); ok { w = int(fw) }
			if fh, ok := mediaArr[2].(float64); ok { h = int(fh) }
		}
		
		// Extract timestamp with improved logic
		timestamp := extractTimestamp(itemArr, extractInt)

		// Description/Caption
		var description string
		for i := 3; i < len(itemArr); i++ {
			if d, ok := itemArr[i].(string); ok && d != "" {
				description = d
				break
			}
		}

		if url != "" {
			photos = append(photos, Photo{
				ID:          id,
				URL:         url,
				Width:       w,
				Height:      h,
				TakenAt:     timestamp,
				Description: description,
			})
		}
	}

	return &Album{
		ID:     url, // Use URL as ID
		Title:  title,
		Photos: photos,
	}, nil
}

// extractTimestamp extracts the best available timestamp from a scraped item
func extractTimestamp(itemArr []interface{}, extractInt func(interface{}) (int64, bool)) time.Time {
	now := time.Now()
	var candidates []int64

	// Collect all plausible timestamps from the item
	for i := 2; i < len(itemArr); i++ {
		if metaArr, ok := itemArr[i].([]interface{}); ok && len(metaArr) > 0 {
			if t, ok := extractInt(metaArr[0]); ok {
				// Must be after 2000-01-01 and not in the future (with 1-day tolerance)
				if t > 946684800000 && time.UnixMilli(t).Before(now.Add(24*time.Hour)) {
					candidates = append(candidates, t)
				}
			}
		}
		// Also check direct numeric values at this index
		if t, ok := extractInt(itemArr[i]); ok {
			if t > 946684800000 && time.UnixMilli(t).Before(now.Add(24*time.Hour)) {
				candidates = append(candidates, t)
			}
		}
	}

	if len(candidates) == 0 {
		return time.Time{}
	}

	// Prefer the oldest valid timestamp (most likely the "taken" date)
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c < best {
			best = c
		}
	}

	return time.UnixMilli(best)
}

// extensionFromContentType maps Content-Type to file extension
func extensionFromContentType(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/heic", "image/heif":
		return ".heic"
	case "image/avif":
		return ".avif"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "video/x-matroska":
		return ".mkv"
	default:
		if strings.HasPrefix(ct, "video/") {
			return ".mp4"
		}
		return ".jpg"
	}
}

// DownloadMedia downloads original media from Google Photos.
// Uses =d for original quality images (preserves motion photo data for Immich), =dv for videos.
// Response is buffered to guarantee accurate Content-Length for the upload.
// Returns: body, size, extension (e.g. ".jpg"), isVideo, error
func DownloadMedia(client *Client, baseUrl string) (io.ReadCloser, int64, string, bool, error) {
	// HEAD probe to detect content type without downloading body
	probeResp, err := client.Head(baseUrl + "=d")
	if err != nil {
		return nil, 0, "", false, err
	}
	probeResp.Body.Close()

	probeCt := probeResp.Header.Get("Content-Type")
	isVideo := strings.HasPrefix(strings.ToLower(probeCt), "video/")

	// Pure video: download with =dv
	if isVideo {
		resp, err := client.Get(baseUrl + "=dv")
		if err != nil {
			return nil, 0, "", false, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, 0, "", false, fmt.Errorf("failed to download video: %d", resp.StatusCode)
		}
		// Buffer video for accurate size
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, 0, "", false, fmt.Errorf("failed to read video data: %w", err)
		}
		ct := resp.Header.Get("Content-Type")
		ext := extensionFromContentType(ct)
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), ext, true, nil
	}

	// Image: download original with =d (motion photos are preserved as-is for Immich)
	resp, err := client.Get(baseUrl + "=d")
	if err != nil {
		return nil, 0, "", false, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, 0, "", false, fmt.Errorf("failed to download image: %d", resp.StatusCode)
	}

	// Buffer to guarantee accurate size (HTTP Content-Length can be -1 for chunked responses)
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, 0, "", false, fmt.Errorf("failed to read image data: %w", err)
	}

	ct := resp.Header.Get("Content-Type")
	ext := extensionFromContentType(ct)
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), ext, false, nil
}
