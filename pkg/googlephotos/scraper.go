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
}

// ScrapeAlbum parses a Google Photos shared album URL and returns the Album structure.
func ScrapeAlbum(url string) (*Album, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}
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
		
		// Metadata (Timestamp)
		// Index 2 is often the array containing timestamp
		var timestamp time.Time
		if len(itemArr) > 2 {
			if metaArr, ok := itemArr[2].([]interface{}); ok && len(metaArr) > 0 {
				// The first element is often the timestamp in ms as string or number
				var tsStr string
				// Check types
				switch v := metaArr[0].(type) {
				case string:
					tsStr = v
				case float64:
					tsStr = fmt.Sprintf("%.0f", v)
				}
				
				if tsStr != "" {
					tsMs, err := strconv.ParseInt(tsStr, 10, 64)
					if err == nil {
						timestamp = time.UnixMilli(tsMs)
					}
				}
			}
		}
		
		// Description/Caption
		// Usually at a later index, e.g., index 5 or inside another object.
		var description string
		if len(itemArr) > 5 {
			if d, ok := itemArr[5].(string); ok {
				description = d
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

func DownloadPhotoStream(url string) (io.ReadCloser, int64, error) {
	// Append =d to get original
	downloadUrl := url + "=d"
	resp, err := http.Get(downloadUrl)
	if err != nil {
		return nil, 0, err
	}
	
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("failed to download photo: %d", resp.StatusCode)
	}
	
	return resp.Body, resp.ContentLength, nil
}
