package app

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"warreth.dev/immich-sync/pkg/config"
	"warreth.dev/immich-sync/pkg/googlephotos"
	"warreth.dev/immich-sync/pkg/immich"
)

type App struct {
	Cfg      *config.Config
	Client   *immich.Client
	GPClient *googlephotos.Client
	Logger   *slog.Logger
}

func New(cfg *config.Config) (*App, error) {
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			if a.Key == slog.TimeKey {
				t := a.Value.Time()
				return slog.String(slog.TimeKey, t.Format("15:04:05"))
			}
			return a
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	client := immich.NewClient(cfg.ApiURL, cfg.ApiKey)
	gpClient := googlephotos.NewClient()
	return &App{
		Cfg:      cfg,
		Client:   client,
		GPClient: gpClient,
		Logger:   logger,
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

	// Schedule Start Time if configured
	if a.Cfg.SyncStartTime != "" {
		now := time.Now()
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
	}

	// Initialize schedule
	nextRun := make(map[string]time.Time)
	for _, ac := range a.Cfg.GooglePhotos {
		nextRun[ac.URL] = time.Now()
	}

	for {
		// Sequential Sync Loop
		for _, ac := range a.Cfg.GooglePhotos {
			if time.Now().After(nextRun[ac.URL]) {
				a.processAlbum(ac)
				
				// Schedule next run
				interval, err := time.ParseDuration(ac.SyncInterval)
				if err != nil || interval == 0 {
					interval = 24 * time.Hour
				}
				nextRun[ac.URL] = time.Now().Add(interval)
				a.Logger.Info("Scheduled next sync", "album", ac.URL, "next_run", nextRun[ac.URL].Format("15:04:05"))
			}
		}

		// Wait before checking again
		time.Sleep(1 * time.Minute)
	}
}

type processResult struct {
	ID          string
	WasUploaded bool
	Error       error
}

func (a *App) processAlbum(ac config.GooglePhotosConfig) {
	logger := a.Logger.With("album_url", ac.URL)
	logger.Info("Syncing Google Photos Album")

	album, err := googlephotos.ScrapeAlbum(a.GPClient, ac.URL)
	if err != nil {
		logger.Error("Error scraping album", "error", err)
		return
	}

	albumTitle := album.Title
	if ac.AlbumName != "" {
		albumTitle = ac.AlbumName
	}
	logger.Info("Found photos in album", "count", len(album.Photos), "title", albumTitle)

	if len(album.Photos) == 0 {
		logger.Info("No photos found, skipping")
		return
	}

	// Resolve Immich album ID
	var albumId string
	if ac.ImmichAlbumID != "" {
		albumId = ac.ImmichAlbumID
	} else {
		albums, err := a.Client.GetAlbums()
		if err == nil {
			for _, a := range albums {
				if a.AlbumName == albumTitle {
					albumId = a.Id
					break
				}
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

	// Pre-fetch existing album assets for O(1) duplicate detection (replaces 6 API calls per photo)
	existingFiles := make(map[string]string) // baseName (no extension) -> asset ID
	if albumId != "" {
		albumDetails, err := a.Client.GetAlbum(albumId)
		if err == nil {
			for _, asset := range albumDetails.Assets {
				name := asset.OriginalFileName
				if dot := strings.LastIndex(name, "."); dot != -1 {
					name = name[:dot]
				}
				existingFiles[name] = asset.Id
			}
			logger.Debug("Pre-fetched album assets", "count", len(existingFiles))
		}
	}

	var newAssetIds []string

	total := len(album.Photos)
	processed := 0
	added := 0
	skipped := 0
	failed := 0

	numWorkers := a.Cfg.Workers
	if numWorkers < 1 {
		numWorkers = 1
	}
	if numWorkers > total {
		numWorkers = total
	}

	logger.Info("Processing items", "total_items", total, "workers", numWorkers)

	jobs := make(chan googlephotos.Photo, total)
	results := make(chan processResult, total)
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				id, uploaded, err := a.processItem(p, albumTitle, ac.URL, existingFiles)
				results <- processResult{ID: id, WasUploaded: uploaded, Error: err}
			}
		}()
	}

	for _, p := range album.Photos {
		jobs <- p
	}
	close(jobs)

	wg.Wait()
	close(results)

	for res := range results {
		processed++
		if res.Error != nil {
			a.Logger.Error("Failed to process item", "error", res.Error)
			failed++
		} else {
			if res.WasUploaded {
				added++
			} else if res.ID == "" {
				skipped++
			}
			if res.ID != "" {
				newAssetIds = append(newAssetIds, res.ID)
			}
		}
	}

	if albumId != "" && len(newAssetIds) > 0 {
		a.Logger.Info("Adding items to album", "count", len(newAssetIds), "album", albumTitle)
		err := a.Client.AddAssetsToAlbum(albumId, newAssetIds)
		if err != nil {
			a.Logger.Error("Error adding assets to album", "error", err)
		}
	}
	a.Logger.Info("Sync finished", "title", albumTitle, "added", added, "skipped", skipped, "failed", failed, "total", processed)
}

func (a *App) processItem(p googlephotos.Photo, albumTitle, albumURL string, existingFiles map[string]string) (string, bool, error) {
	safeId := strings.ReplaceAll(p.ID, "/", "_")
	safeId = strings.ReplaceAll(safeId, ":", "_")
	baseName := fmt.Sprintf("gp_%s", safeId)

	// O(1) check against pre-fetched album assets (replaces 6 API calls per photo)
	if assetId, exists := existingFiles[baseName]; exists {
		a.Logger.Debug("Asset already in album", "id", assetId, "filename", baseName)
		return "", false, nil
	}

	if a.Cfg.StrictMetadata && p.TakenAt.IsZero() {
		a.Logger.Warn("Skipping item with missing metadata date",
			"id", p.ID, "url", p.URL)
		return "", false, nil
	}

	// Download original media from Google Photos (=d for original quality)
	a.Logger.Debug("Downloading item", "id", safeId)
	r, size, ext, isVideo, err := googlephotos.DownloadMedia(a.GPClient, p.URL)
	if err != nil {
		return "", false, fmt.Errorf("error downloading item: %w", err)
	}

	if isVideo && a.Cfg.SkipVideos {
		r.Close()
		a.Logger.Debug("Skipping video item", "id", p.ID)
		return "", false, nil
	}

	filename := baseName + ext

	// Build description with source metadata
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
	description += fmt.Sprintf("%sSource Album: %s (%s)", sep, albumTitle, albumURL)

	if p.TakenAt.IsZero() {
		a.Logger.Warn("Uploading item with missing metadata date (using current time)",
			"id", safeId, "url", p.URL, "is_video", isVideo)
	}

	uploadedId, isDup, err := a.Client.UploadAssetStream(r, filename, size, p.TakenAt, description)
	r.Close()
	if err != nil {
		return "", false, fmt.Errorf("error uploading %s: %w", filename, err)
	}
	if uploadedId == "" {
		return "", false, fmt.Errorf("upload returned empty ID for %s", filename)
	}

	if isDup {
		a.Logger.Debug("Asset deduplicated by Immich", "filename", filename, "id", uploadedId)
		return uploadedId, false, nil
	}

	a.Logger.Debug("Uploaded item", "filename", filename, "id", uploadedId)
	return uploadedId, true, nil
}

