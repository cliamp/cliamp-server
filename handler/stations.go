package handler

import (
	"encoding/json"
	"net/http"
)

// StationEntry is one station in the public station directory. Stream and
// TracksURL hold paths. The handler makes them absolute for each request.
type StationEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Stream      string `json:"stream"`
	Tracks      int    `json:"tracks"`
	TracksURL   string `json:"tracks_url,omitempty"`
}

// Stations handles GET /stations - every station in config file order, with
// its stream URL and the number of tracks it exposes. A client reads one
// document to learn which stations it can open as a playlist.
type Stations struct {
	Entries []StationEntry
}

func (s *Stations) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	entries := make([]StationEntry, len(s.Entries))
	for i, e := range s.Entries {
		e.Stream = base + e.Stream
		if e.TracksURL != "" {
			e.TracksURL = base + e.TracksURL
		}
		entries[i] = e
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Stations []StationEntry `json:"stations"`
	}{Stations: entries})
}
