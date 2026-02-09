# GPhotos_To_Immich

Sync photos from Google Photos Shared Albums to your Immich instance.

## Quick Start (Docker)

1. **Configure**
   Download & Copy `config.example.json` to `config.json` and add your Immich details and Google Photos links.

2. **Run**
   The container is available on GitHub Container Registry (ghcr.io).
   
   Create a `compose.yml`:
   ```yaml
   services:
     immich-sync:
       image: ghcr.io/warreth/gphotosalbum_to_immich:latest
       container_name: immich-sync
       restart: unless-stopped
       volumes:
         - ./config.json:/app/config.json
   ```
   
   > **Note:** You can also configure using environment variables (e.g. `IMMICH_API_KEY`) if you prefer not to mount a config file.

   Then run:
   ```bash
   docker compose up -d
   ```

## Configuration (`config.json`)

### API Permissions
If you are generating a specific API key for this tool, ensure it has the following permissions:
- `asset.read`
- `asset.upload`
- `album.create`
- `album.read`
- `album.update`
- `albumAsset.create`
- `user.read`

Alternatively, you can use a key with "All" permissions.

```json
{
    "apiKey": "YOUR_IMMICH_API_KEY",
    "apiURL": "http://your-immich-ip:2283/api",
    "debug": false,
    "workers": 4,
    "syncStartTime": "02:00",
    "googlePhotos": [
        {
            "url": "https://photos.app.goo.gl/YourAlbumLink1",
            "syncInterval": "12h",
            "albumName": "Vacation 2023"
        }
    ]
}
```

### Options
| Key | Type | Description |
| --- | --- | --- |
| `apiKey` | string | Immich API Key |
| `apiURL` | string | Immich API URL (e.g. `http://localhost:2283/api`) |
| `workers` | int | (Optional) Number of concurrent download/upload workers per album (default: `1`). |
| `debug` | bool | Enable verbose logging (default: `false` for essential logs only) |
| `syncStartTime` | string | (Optional) Daily start time in `HH:MM` format. If set, the app waits until this time to run the first sync. |
| `googlePhotos[].syncInterval` | string | Interval between checks (e.g. `12h`, `60m`). Default `24h`. |

## Features
- **Shared Albums**: Syncs photos directly from shared links.
- **Efficient Processing**: Streaming uploads with minimal resource usage.
- **Concurrent Workers**: Support for parallel downloading/uploading to speed up large albums.
- **Sequential Syncing**: Processes configured albums one by one to manage load.
- **Smart Date Detection**: Improved metadata parsing to find the original "taken" date instead of upload date.
- **Rate Limit Protection**: Built-in jitter and intelligent retry logic to avoid Google Photos rate limits.
- **Background Sync**: Runs continuously on a schedule.

- **Respects Trash**: Items previously moved to trash in Immich are detected and skipped, not re-uploaded.
## Manual Run (Dev)

```bash
go run main.go
```
