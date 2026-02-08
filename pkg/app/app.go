package app

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"webgears.org/immich-sync/pkg/config"
	"webgears.org/immich-sync/pkg/googlephotos"
	"webgears.org/immich-sync/pkg/immich"
)

type App struct {
	Cfg    *config.Config
	Client *immich.Client
	Logger *slog.Logger
}

func New(cfg *config.Config) (*App, error) {
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{
		Level: level,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	client := immich.NewClient(cfg.ApiURL, cfg.ApiKey)
	return &App{
		Cfg:    cfg,
		Client: client,
		Logger: logger,
	}, nil
}

func (a *App) Run() {
	a.Logger.Info("Starting Immich Sync")

	id, name, err := a.Client.GetUser()
	if err != nil {
		a.Logger.Error("Failed to connect to Immich", "error", err)
		os.Exit(1)
	}
	a.Logger.Info("Connected to Immich", "user_id", id, "name", name)

	if len(a.Cfg.GooglePhotos) == 0 {
		a.Logger.Warn("No albums configured")
		return
	}

	forever := make(chan struct{})

	for _, ac := range a.Cfg.GooglePhotos {
		go a.syncLoop(ac)
	}

	<-forever
}

func (a *App) syncLoop(ac config.GooglePhotosConfig) {
	interval, err := time.ParseDuration(ac.SyncInterval)
	if err != nil || interval == 0 {
		interval = 24 * time.Hour
	}

	// Schedule Start Time if configured
	if a.Cfg.SyncStartTime != "" {
		now := time.Now()
		// Try parsing "15:04"
		parsedTime, err := time.Parse("15:04", a.Cfg.SyncStartTime)
		if err != nil {
			a.Logger.Error("Invalid syncStartTime format, expected HH:MM", "error", err)
		} else {
			// Construct the next occurrence
			nextRun := time.Date(now.Year(), now.Month(), now.Day(), parsedTime.Hour(), parsedTime.Minute(), 0, 0, now.Location())
			if nextRun.Before(now) {
				nextRun = nextRun.Add(24 * time.Hour)
			}
			delay := nextRun.Sub(now)
			a.Logger.Info("Waiting for scheduled start time", "start_time", a.Cfg.SyncStartTime, "delay", delay.Round(time.Second).String())
			time.Sleep(delay)
		}
	} else {
		a.Logger.Info("Scheduled sync", "album", ac.URL, "interval", interval.String())
		// Run immediately if no start time enforced
		a.runGPhotoSync(ac)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// First run if we waited for custom time
		if a.Cfg.SyncStartTime != "" {
			a.runGPhotoSync(ac)
		}
		
		<-ticker.C
		// If starttime was not set, this is just normal periodic loop
		// If starttime was set, subsequent ticks happen at 'interval' from the first run.
		if a.Cfg.SyncStartTime == "" {
			a.Logger.Info("Starting scheduled sync check", "album", ac.URL)
			a.runGPhotoSync(ac)
		} else {
			// Logic: The ticker fires AFTER interval. 
			// If we just ran "manually" before the loop due to sleep, we should verify logic.
			// Actually, standard Ticker behavior works well: `Tick at T+Interval`. 
			// So if we slept until 02:00 and ran, the ticker created then (or reset? no we need to create it after invalidating drift?)
			// Creating ticker AFTER the sleep ensures alignment.
		}
	}
}

func (a *App) runGPhotoSync(ac config.GooglePhotosConfig) {
	logger := a.Logger.With("album_url", ac.URL)
	logger.Info("Syncing Google Photos Album")

	album, err := googlephotos.ScrapeAlbum(ac.URL)
	if err != nil {
		logger.Error("Error scraping album", "error", err)
		return
	}

	albumTitle := album.Title
	if ac.AlbumName != "" {
		albumTitle = ac.AlbumName
	}
	logger.Info("Found photos in album", "count", len(album.Photos), "title", albumTitle)

	// Ensure Immich Album exists
	var albumId string

	if ac.ImmichAlbumID != "" {
		albumId = ac.ImmichAlbumID
	} else {
		// Find or Create by Name
		albums, _ := a.Client.GetAlbums()
		for _, a := range albums {
			if a.AlbumName == albumTitle {
				albumId = a.Id
				break
			}
		}
		if albumId == "" {
			logger.Info("Creating Immich album", "title", albumTitle)
			newAlbum, err := a.Client.CreateAlbum(albumTitle)
			if err == nil {
				albumId = newAlbum.Id
			} else {
				logger.Error("Error creating album", "error", err)
			}
		}
	}

	var newAssetIds []string
	
	// Stats
	total := len(album.Photos)
	processed := 0
	added := 0
	failed := 0
	startTime := time.Now()

	a.Logger.Info("Processing photos", "total_items", total)

	for _, p := range album.Photos {
		processed++
		
		// Create a deterministic filename
		safeId := strings.ReplaceAll(p.ID, "/", "_")
		safeId = strings.ReplaceAll(safeId, ":", "_")
		fakeFilename := fmt.Sprintf("gp_%s.jpg", safeId)

		// Search in Immich
		exists, _ := a.Client.SearchAssets(fakeFilename)
		if len(exists) > 0 {
			// Found it.
			newAssetIds = append(newAssetIds, exists[0].Id)
			a.Logger.Debug("Asset already exists locally", "id", exists[0].Id)
			// Logic continue...
		} else {
			// Download logic...
			a.Logger.Debug("Downloading new photo", "id", safeId)
			
			// Use Streaming Download & Upload
			r, size, err := googlephotos.DownloadPhotoStream(p.URL)
			if err != nil {
				a.Logger.Error("Error downloading photo", "id", safeId, "error", err)
				failed++
				goto ProgressCheck
			}

			// Build Description
			description := p.Description
			if p.Uploader != "" {
				if description != "" {
					description += "\n\n"
				}
				description += fmt.Sprintf("Shared by: %s", p.Uploader)
			}
			
			sep := "\n"
			if description != "" {
				sep = "\n\n"
			}
			description += fmt.Sprintf("%sSource Album: %s (%s)", sep, album.Title, ac.URL)

			// Upload with metadata
	        // Note: size is int64, which is correct for UploadAssetStream
			uploadedId, isDup, err := a.Client.UploadAssetStream(r, fakeFilename, size, p.TakenAt, description)
			r.Close() // Close response body
			
			if err != nil {
				a.Logger.Error("Error uploading photo", "filename", fakeFilename, "error", err)
				failed++
				goto ProgressCheck
			}

			if uploadedId != "" {
				if isDup {
					a.Logger.Debug("Photo already exists (deduplicated)", "filename", fakeFilename, "id", uploadedId)
				} else {
					a.Logger.Debug("Uploaded photo", "filename", fakeFilename, "id", uploadedId)
					added++
				}
				newAssetIds = append(newAssetIds, uploadedId)
			}
		}

	ProgressCheck:
		// Progress update every 10 items or 10%
		if total > 0 && (processed%10 == 0 || processed == total) {
			elapsed := time.Since(startTime)
			rate := float64(processed) / elapsed.Seconds()
			remaining := total - processed
			eta := time.Duration(float64(remaining)/rate) * time.Second
			a.Logger.Info("Progress", "processed", processed, "total", total, "eta", eta.Round(time.Second).String(), "failed", failed)
		}
	}

	if albumId != "" && len(newAssetIds) > 0 {
		a.Logger.Info("Adding items to album", "count", len(newAssetIds), "album", albumTitle)
		err := a.Client.AddAssetsToAlbum(albumId, newAssetIds)
		if err != nil {
			a.Logger.Error("Error adding assets to album", "error", err)
		}
	}
	a.Logger.Info("Sync finished", "title", albumTitle, "added", added, "failed", failed, "total_processed", processed)
}
