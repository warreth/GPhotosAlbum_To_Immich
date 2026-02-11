package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type GooglePhotosConfig struct {
	URL           string `json:"url"`
	ImmichAlbumID string `json:"immichAlbumId"`     // Optional, if existing
	AlbumName     string `json:"albumName"`         // Optional, to create new
	SyncInterval  string `json:"syncInterval"`      // e.g., "12h", "60m"
}

type Config struct {
	ApiKey         string               `json:"apiKey"`
	ApiURL         string               `json:"apiURL"`
	Debug          bool                 `json:"debug"`          // Optional, enable verbose logging
	Workers        int                  `json:"workers"`        // Optional, default 1
	SyncStartTime  string               `json:"syncStartTime"`  // Optional, e.g. "02:00" (24h format)
	StrictMetadata bool                 `json:"strictMetadata"` // Optional, skip items with missing dates
	SkipVideos     bool                 `json:"skipVideos"`     // Optional, skip video items entirely
	GooglePhotos   []GooglePhotosConfig `json:"googlePhotos"`
}

func ReadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try ENV vars for basic config
			apiKey := os.Getenv("IMMICH_API_KEY")
			apiURL := os.Getenv("IMMICH_API_URL")
			
			if apiKey == "" || apiURL == "" {
				return nil, fmt.Errorf("config file not found and ENV vars missing")
			}
			
			return &Config{
				ApiKey: apiKey,
				ApiURL: apiURL,
			}, nil
		}
		return nil, err
	}
	defer file.Close()
	
	bytefile, _ := io.ReadAll(file)
	var config Config
	if err := json.Unmarshal(bytefile, &config); err != nil {
		return nil, err
	}
	
	// Override/Fallback with ENV
	if config.ApiKey == "" { config.ApiKey = os.Getenv("IMMICH_API_KEY") }
	if config.ApiURL == "" { config.ApiURL = os.Getenv("IMMICH_API_URL") }
	
	return &config, nil
}
