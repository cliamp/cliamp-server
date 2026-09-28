package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cliamp-server/library"
	"cliamp-server/stats"
)

func TestTracksJSONIncludesPlayCounts(t *testing.T) {
	db, err := stats.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	idx := NewTrackIndex("lofi", "Lo-fi", []library.Track{
		{Path: "/music/first.mp3", Title: "First"},
		{Path: "/music/second.mp3", Title: "Second"},
	})
	for range 2 {
		if err := db.RecordTrackPlay(stats.TrackPlay{Station: "lofi", TrackID: idx.entries[0].ID, PlayedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "https://radio.example/lofi/tracks", nil)
	rec := httptest.NewRecorder()
	(&TracksJSON{Index: idx, StatsDB: db}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got struct {
		Tracks []TrackEntry `json:"tracks"`
	}
	body := rec.Body.Bytes()
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(got.Tracks))
	}
	if !strings.Contains(string(body), `"plays": 0`) {
		t.Error("response does not include a zero plays field")
	}
	if got.Tracks[0].Plays != 2 {
		t.Errorf("first plays = %d, want 2", got.Tracks[0].Plays)
	}
	if got.Tracks[1].Plays != 0 {
		t.Errorf("second plays = %d, want 0", got.Tracks[1].Plays)
	}
}

func TestTrackFileRecordsInitialRequests(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.mp3")
	if err := os.WriteFile(path, []byte("audio data"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := stats.Open(filepath.Join(dir, "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	idx := NewTrackIndex("lofi", "Lo-fi", []library.Track{{Path: path, Title: "Track"}})
	id := idx.entries[0].ID
	h := &TrackFile{Index: idx, StatsDB: db}

	tests := []struct {
		name        string
		method      string
		rangeHeader string
		plays       int64
	}{
		{name: "full request", method: http.MethodGet, plays: 1},
		{name: "seek", method: http.MethodGet, rangeHeader: "bytes=5-", plays: 1},
		{name: "initial range", method: http.MethodGet, rangeHeader: "bytes=0-", plays: 2},
		{name: "head", method: http.MethodHead, plays: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/lofi/tracks/"+id, nil)
			req.SetPathValue("id", id)
			if tt.rangeHeader != "" {
				req.Header.Set("Range", tt.rangeHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK && rec.Code != http.StatusPartialContent {
				t.Fatalf("status = %d, want successful response", rec.Code)
			}
			counts, err := db.TrackPlayCounts("lofi")
			if err != nil {
				t.Fatal(err)
			}
			if counts[id] != tt.plays {
				t.Errorf("plays = %d, want %d", counts[id], tt.plays)
			}
		})
	}
}
