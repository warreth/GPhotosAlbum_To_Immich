# Immich Sync

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
       image: ghcr.io/warreth/immich-sync:latest
       container_name: immich-sync
       restart: unless-stopped
       volumes:
         - ./config.json:/app/config.json
   ```

   Then run:
   ```bash
   docker compose up -d
   ```

## Configuration (`config.json`)

### API Permissions
If you are generating a specific API key for this tool, ensure it has the following permissions:
- `user.read`
- `asset.read`
- `asset.create`
- `album.read`
- `album.create`
- `album.update`

Alternatively, you can use a key with "All" permissions.

```json
{
    "apiKey": "YOUR_IMMICH_API_KEY",
    "apiURL": "http://your-immich-ip:2283/api",
    "debug": false,
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
| `debug` | bool | Enable verbose logging (default: `false` for essential logs only) |
| `syncStartTime` | string | (Optional) Daily start time in `HH:MM` format. If set, the app waits until this time to run the first sync. |
| `googlePhotos` | array | List of albums to sync |
| `googlePhotos[].syncInterval` | string | Interval between checks (e.g. `12h`, `60m`). Default `24h`. |

## Features
- **Shared Albums**: Syncs photos directly from shared links.
- **Efficient**: Streaming uploads with minimal resource usage and no disk writes.
- **Background Sync**: Runs continuously on a schedule.

## Manual Run (Dev)

```bash
go run main.go
```
