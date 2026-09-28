# Features

## Audio Format Support

Every station emits one continuous 128 kbps, 44.1 kHz stereo MP3 stream. Source files are decoded to a fixed PCM profile and passed through a long-lived station encoder. This keeps track transitions gapless and prevents bitrate or sample-rate changes from interrupting players.

| Format | Extension | Transcoded |
|--------|-----------|------------|
| MP3 | `.mp3` | Yes |
| WAV | `.wav` | Yes |
| FLAC | `.flac` | Yes |
| OGG Vorbis | `.ogg` | Yes |
| Opus | `.opus` | Yes |
| AAC/M4A | `.m4a`, `.aac` | Yes |
| WebM | `.webm` | Yes |
| WMA | `.wma` | Yes |

Streaming requires `ffmpeg` with `libmp3lame` to be installed and available on `PATH`.

Track metadata (title, artist, album) is read from ID3 tags. Files without tags fall back to the filename as the title.

## Ad Scheduling

Ads are injected between music tracks based on two independent triggers. Either one firing will cause the next track to be an ad:

1. **Song count**: After every N music tracks (`ad_every_n_songs`)
2. **Time interval**: After every N minutes (`ad_every_n_minutes`)

Set `ads_path` to a directory of supported audio files or a single audio file. When `ad_shuffle` is true, ads are played in random order. When false, they rotate sequentially.

Both triggers reset after each ad plays.

## GeoIP

Listener IP geolocation uses a `.mmdb` database. When configured, each connected listener's IP is resolved to a country, city, and coordinates, visible in the status JSON.

To enable it:

1. Download the [DB-IP City Lite](https://download.db-ip.com/free/dbip-city-lite-2026-03.mmdb.gz) database (free, no signup)
2. Unpack it: `gunzip dbip-city-lite-2026-03.mmdb.gz`
3. Set the path via `--geo-db /path/to/dbip-city-lite-2026-03.mmdb` or in the config file under `[geo] db_path`

Client IP is extracted from `X-Forwarded-For`, then `X-Real-IP`, then the connection's remote address. This works correctly behind reverse proxies that set these headers.

## Listener Statistics

When a SQLite database path is configured, the server records each listener session on disconnect. No IP addresses are stored — only geo information (country, city, coordinates), connection times, and duration.

To enable it:

1. Pass `--stats-db /path/to/stats.db` or set `[stats] db_path` in the config file
2. The database file is created automatically; the parent directory must exist

The public `/statistics` and `/<id>/statistics` endpoints return aggregated data: total sessions, listen hours, top 10 countries, top 10 cities, and a daily breakdown for the last 30 days. These endpoints require no authentication.

Statistics work best when combined with GeoIP — without it, country and city fields will be empty.

### Track Statistics

For stations with `expose_tracks = true`, the server also records each play of an exposed track. A play is a `GET` request for a track file that starts at byte 0. A repeat request for the same track from the same client within 30 seconds is not a new play. The server stores the track, the time, and the geo information of the client. It does not store the IP address.

The public `/tracks/statistics` and `/<id>/tracks/statistics` endpoints return total plays, the all-time track listener peak, active track listeners, the top 10 countries and cities, the top 10 tracks, and a daily breakdown for the last 30 days. A client counts as an active track listener for 10 minutes after it starts a track. See [API](api.md#track-statistics) for the response format.
