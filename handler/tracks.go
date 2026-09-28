package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cliamp-server/geo"
	"cliamp-server/library"
	"cliamp-server/stats"
)

// audioMIME covers the extensions the library scanner accepts but that the
// system mime table often does not know about.
var audioMIME = map[string]string{
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".flac": "audio/flac",
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".webm": "audio/webm",
	".wma":  "audio/x-ms-wma",
}

// TrackEntry is one track as exposed over HTTP. It deliberately omits the
// on-disk path so the filesystem layout is never published.
type TrackEntry struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist,omitempty"`
	Album    string  `json:"album,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Filename string  `json:"filename"`
	URL      string  `json:"url"`
	Plays    int64   `json:"plays"`
}

// TrackIndex holds the per-station track listing and the id-to-path lookup
// used to serve individual files. The map is built once at startup, so a
// request can only ever name a track that was scanned into the library.
type TrackIndex struct {
	StationID   string
	StationName string

	entries []TrackEntry
	paths   map[string]string // track id -> absolute path
}

// NewTrackIndex builds the exposed listing for one station.
func NewTrackIndex(stationID, stationName string, tracks []library.Track) *TrackIndex {
	idx := &TrackIndex{
		StationID:   stationID,
		StationName: stationName,
		entries:     make([]TrackEntry, 0, len(tracks)),
		paths:       make(map[string]string, len(tracks)),
	}

	for _, t := range tracks {
		id := trackID(t.Path)
		// A duplicate id means the same absolute path was scanned twice;
		// keep the first and skip the rest so ids stay unambiguous.
		if _, seen := idx.paths[id]; seen {
			continue
		}
		idx.paths[id] = t.Path

		title := t.Title
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(t.Path), filepath.Ext(t.Path))
		}

		idx.entries = append(idx.entries, TrackEntry{
			ID:       id,
			Title:    title,
			Artist:   t.Artist,
			Album:    t.Album,
			Duration: t.Duration,
			Filename: filepath.Base(t.Path),
		})
	}

	return idx
}

// Len reports how many tracks the index exposes.
func (idx *TrackIndex) Len() int { return len(idx.entries) }

// trackID derives a stable, opaque id from a track's absolute path. It is
// stable across restarts so links keep working, and it is not reversible into
// a path by a caller.
func trackID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// baseURL returns the scheme and host the request arrived on, honouring a
// reverse proxy's X-Forwarded-Proto.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	return scheme + "://" + r.Host
}

// withURLs returns the entries with absolute URLs filled in for this request.
func (idx *TrackIndex) withURLs(r *http.Request, playCounts map[string]int64) []TrackEntry {
	base := baseURL(r) + "/" + idx.StationID + "/tracks/"
	out := make([]TrackEntry, len(idx.entries))
	for i, e := range idx.entries {
		e.URL = base + e.ID
		e.Plays = playCounts[e.ID]
		out[i] = e
	}
	return out
}

// TracksJSON handles GET /{station}/tracks - the track listing.
type TracksJSON struct {
	Index   *TrackIndex
	StatsDB *stats.DB
}

func (h *TracksJSON) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var playCounts map[string]int64
	if h.StatsDB != nil {
		var err error
		playCounts, err = h.StatsDB.TrackPlayCounts(h.Index.StationID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	entries := h.Index.withURLs(r, playCounts)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Station string       `json:"station"`
		Name    string       `json:"name"`
		Count   int          `json:"count"`
		Tracks  []TrackEntry `json:"tracks"`
	}{
		Station: h.Index.StationID,
		Name:    h.Index.StationName,
		Count:   len(entries),
		Tracks:  entries,
	})
}

// TracksM3U handles GET /{station}/tracks.m3u - every song as a direct URL,
// so any player can open the station's library rather than the live stream.
type TracksM3U struct {
	Index *TrackIndex
}

func (h *TracksM3U) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entries := h.Index.withURLs(r, nil)

	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.Header().Set("Content-Disposition", "inline; filename=\"tracks.m3u\"")

	fmt.Fprint(w, "#EXTM3U\n")
	for _, e := range entries {
		label := e.Title
		if e.Artist != "" {
			label = e.Artist + " - " + e.Title
		}
		// -1 is the M3U convention for an unknown duration; the library
		// scanner does not decode files, so Duration is often 0.
		secs := -1
		if e.Duration > 0 {
			secs = int(e.Duration)
		}
		fmt.Fprintf(w, "#EXTINF:%d,%s\n%s\n", secs, label, e.URL)
	}
}

// TrackFile handles GET /{station}/tracks/{id} - one audio file.
type TrackFile struct {
	Index     *TrackIndex
	StatsDB   *stats.DB
	GeoDB     *geo.DB         // Optional geo database (nil = no geo lookup)
	Listeners *TrackListeners // Optional active listener tracker
}

func (h *TrackFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	path, ok := h.Index.paths[id]
	if !ok {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		// The library was scanned at startup; the file may have been moved
		// since. Report it as missing rather than leaking the reason.
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(path))
	ctype := audioMIME[ext]
	if ctype == "" {
		ctype = mime.TypeByExtension(ext)
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}

	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Accept-Ranges", "bytes")
	// Inline so browsers play it in place; the filename is still offered for
	// anyone who chooses to save it.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("inline; filename*=UTF-8''%s", urlEncode(filepath.Base(path))))

	if h.StatsDB != nil && isTrackPlayRequest(r) {
		h.recordPlay(r, id)
	}

	// ServeContent handles Range requests, If-Modified-Since and Content-Length.
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}

// recordPlay stores one play of track id with the client's location.
func (h *TrackFile) recordPlay(r *http.Request, id string) {
	now := time.Now()
	ip := clientIP(r)

	var loc geo.Location
	if h.GeoDB != nil {
		loc = h.GeoDB.Lookup(ip)
	}

	play := stats.TrackPlay{
		Station:     h.Index.StationID,
		TrackID:     id,
		Country:     loc.Country,
		CountryCode: loc.CountryCode,
		City:        loc.City,
		Latitude:    loc.Latitude,
		Longitude:   loc.Longitude,
		PlayedAt:    now,
	}
	if h.Listeners != nil {
		play.Listeners, play.TotalListeners = h.Listeners.Touch(h.Index.StationID, ip, loc, now)
	}

	if err := h.StatsDB.RecordTrackPlay(play); err != nil {
		slog.Error("failed to record track play", "station", h.Index.StationID, "track_id", id, "error", err)
	}
}

// isTrackPlayRequest excludes HEAD requests and seeks into the middle of a
// track from the play count.
func isTrackPlayRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rangeHeader := strings.ToLower(strings.TrimSpace(r.Header.Get("Range")))
	return rangeHeader == "" || strings.HasPrefix(rangeHeader, "bytes=0-")
}

// urlEncode percent-encodes a filename for a Content-Disposition header.
func urlEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
