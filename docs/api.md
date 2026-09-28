# API

## Stream Endpoints

Each station exposes three endpoints under its ID prefix:

| Endpoint | Content Type | Description |
|----------|-------------|-------------|
| `/<id>/stream` | `audio/mpeg` | Live MP3 audio stream with ICY metadata |
| `/<id>/stream.pls` | `audio/x-scpls` | PLS playlist file pointing to the stream |
| `/<id>/stream.m3u` | `audio/x-mpegurl` | M3U playlist file pointing to the stream |
| `/streams.pls` | `audio/x-scpls` | PLS playlist file listing all stations |
| `/streams.m3u` | `audio/x-mpegurl` | M3U playlist file listing all stations |
| `/logo.svg` | `image/svg+xml` | CLIAMP station logo for players and directory listings |

The stream endpoint supports ICY metadata. Clients that send the `Icy-MetaData: 1` request header receive inline metadata blocks containing the current track title and artist.

Response headers include `icy-name`, `icy-genre`, `icy-br` (bitrate), `icy-sr` (sample rate), and `icy-metaint` (metadata interval). The `icy-logo` extension contains an absolute URL to `/logo.svg`; use the same URL as the station favicon when registering it with a radio directory.

## Track Endpoints

Available for stations configured with `expose_tracks = true`.

| Endpoint | Description |
|----------|-------------|
| `/<id>/tracks` | JSON library listing with a persistent `plays` count for each track |
| `/<id>/tracks.m3u` | M3U playlist containing every track |
| `/<id>/tracks/<track-id>` | Direct audio file with range request support |
| `/<id>/tracks/statistics` | Aggregated play statistics for the exposed tracks of one station |
| `/tracks/statistics` | Aggregated play statistics for the exposed tracks of all stations |

Play counts and track statistics require `--stats-db`. A play is counted when a `GET` request starts at byte 0. `HEAD` requests and seeks into the middle of a track are not counted. Without a statistics database, every track reports `"plays": 0` and the statistics routes return 404.

### Track Statistics

The track statistics endpoints are public and contain no IP addresses. They use the same structure as the radio statistics, with plays in place of sessions.

```
curl http://localhost:8000/radio/tracks/statistics
curl http://localhost:8000/tracks/statistics
```

The server sees file requests, not playback. For this reason, the fields have these meanings:

- `total_plays` counts every recorded play, including plays from before the upgrade that added track statistics.
- `active_listeners` counts the client IP addresses that started a track on the station in the last 10 minutes.
- `peak_listeners` is the all-time highest `active_listeners` value. Track peaks are separate from radio peaks.
- `top_countries`, `top_cities` and `daily` count plays. They include only plays recorded after the upgrade. Country and city fields require GeoIP.
- `top_tracks` lists up to 10 tracks with the most plays, in the same format as `/<id>/tracks`.
- Track statistics have no listen hours, because the server cannot measure playback time for a downloaded file.

Per-station response:

```json
{
  "total_plays": 1520,
  "peak_listeners": 14,
  "active_listeners": 3,
  "active_listener_countries": [
    { "country": "Norway", "country_code": "NO", "listeners": 2 }
  ],
  "top_countries": [
    { "country": "Norway", "country_code": "NO", "plays": 410 }
  ],
  "top_cities": [
    { "city": "Oslo", "country_code": "NO", "plays": 120 }
  ],
  "top_tracks": [
    {
      "id": "9a5501dc8bfe5544",
      "title": "Song Title",
      "artist": "Artist Name",
      "filename": "song.mp3",
      "url": "https://radio.example/radio/tracks/9a5501dc8bfe5544",
      "plays": 87
    }
  ],
  "daily": [
    { "date": "2026-09-28", "plays": 64 }
  ]
}
```

Global response:

```json
{
  "total_plays": 2210,
  "peak_listeners": 19,
  "stations": {
    "radio": {
      "total_plays": 1520,
      "peak_listeners": 14,
      "active_listeners": 3,
      "active_listener_countries": [],
      "top_countries": [],
      "top_cities": [],
      "top_tracks": [],
      "daily": []
    }
  }
}
```

## Status Endpoints

| Endpoint | Description |
|----------|-------------|
| `/<id>/status` | JSON status for a single station |
| `/status` | JSON status for all stations |

If `[admin] password` is set, status endpoints require a Bearer token:

```
curl -H "Authorization: Bearer yourpassword" http://localhost:8000/status
```

### Per-Station Status Response

```json
{
  "station": "Pop Station",
  "favicon": "https://radio.example/logo.svg",
  "listeners": 12,
  "listener_details": [
    {
      "ip": "203.0.113.42",
      "country": "Norway",
      "country_code": "NO",
      "city": "Oslo",
      "latitude": 59.9139,
      "longitude": 10.7522,
      "connected_at": "2026-03-04T10:30:00Z",
      "duration_seconds": 300
    }
  ],
  "current_track": {
    "title": "Song Title",
    "artist": "Artist Name",
    "album": "Album Name"
  },
  "uptime": "5h30m45s",
  "uptime_seconds": 19845,
  "playlist_length": 247
}
```

### Global Status Response

```json
{
  "stations": {
    "pop": {
      "name": "Pop Station",
      "favicon": "https://radio.example/logo.svg",
      "listeners": 12,
      "listener_details": [],
      "current_track": { "title": "", "artist": "", "album": "" },
      "playlist_length": 247
    },
    "jazz": {
      "name": "Jazz Station",
      "favicon": "https://radio.example/logo.svg",
      "listeners": 3,
      "listener_details": [],
      "current_track": { "title": "", "artist": "", "album": "" },
      "playlist_length": 89
    }
  },
  "total_listeners": 15,
  "uptime": "5h30m45s",
  "uptime_seconds": 19845
}
```

## Statistics Endpoints

Available when `--stats-db` is configured. These are **public** (no password required) and contain no IP addresses.

| Endpoint | Description |
|----------|-------------|
| `/<id>/statistics` | Aggregated listener statistics for a single station, including its all-time listener high |
| `/statistics` | Aggregated listener statistics for all stations, including the global all-time listener high |

```
curl http://localhost:8000/radio/statistics
curl http://localhost:8000/statistics
```

### Per-Station Statistics Response

```json
{
  "total_sessions": 8234,
  "total_listen_hours": 2810.3,
  "peak_listeners": 68,
  "active_listeners": 42,
  "top_countries": [
    { "country": "Norway", "country_code": "NO", "sessions": 3200, "listen_hours": 1100.2 }
  ],
  "top_cities": [
    { "city": "Oslo", "country_code": "NO", "sessions": 800, "listen_hours": 280.5 }
  ],
  "daily": [
    { "date": "2026-03-05", "sessions": 150, "listen_hours": 48.2 }
  ]
}
```

### Global Statistics Response

```json
{
  "total_sessions": 12847,
  "total_listen_hours": 4231.5,
  "peak_listeners": 86,
  "stations": {
    "pop": {
      "total_sessions": 8234,
      "total_listen_hours": 2810.3,
      "peak_listeners": 68,
      "active_listeners": 42,
      "top_countries": [],
      "top_cities": [],
      "daily": []
    }
  }
}
```
