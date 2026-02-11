package googlephotos

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
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
func ScrapeAlbum(url string) (*Album, error) {
	client := NewClient()
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

// DownloadMediaStream downloads media from Google Photos, detecting videos vs photos.
// Uses a HEAD request to probe Content-Type first, then downloads with the correct parameter.
// Returns: body, size, isVideo, error
func DownloadMediaStream(baseUrl string, hintIsVideo bool) (io.ReadCloser, int64, bool, error) {
	client := NewClient()
	isVideo := false

	// Probe with HEAD request to =dv to check if this item is a video
	videoUrl := baseUrl + "=dv"
	probeReq, err := http.NewRequest("HEAD", videoUrl, nil)
	if err == nil {
		probeReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		probeResp, probeErr := client.client.Do(probeReq)
		if probeErr == nil {
			ct := probeResp.Header.Get("Content-Type")
			probeResp.Body.Close()
			if probeResp.StatusCode == 200 && strings.HasPrefix(ct, "video/") {
				isVideo = true
			}
		}
	}

	// Download with the correct parameter
	var downloadUrl string
	if isVideo {
		downloadUrl = baseUrl + "=dv"
	} else {
		downloadUrl = baseUrl + "=d"
	}

	req, err := http.NewRequest("GET", downloadUrl, nil)
	if err != nil {
		return nil, 0, false, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, false, err
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, 0, false, fmt.Errorf("failed to download media: %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, isVideo, nil
}
