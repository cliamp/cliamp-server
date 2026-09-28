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

	"cliamp-server/geo"
	"cliamp-server/library"
	"cliamp-server/stats"
)

func TestTrackListenersExpireAfterWindow(t *testing.T) {
	listeners := NewTrackListeners()
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	norway := geo.Location{Country: "Norway", CountryCode: "NO"}

	if n, total := listeners.Touch("lofi", "203.0.113.1", norway, start); n != 1 || total != 1 {
		t.Fatalf("first touch = (%d, %d), want (1, 1)", n, total)
	}
	if n, total := listeners.Touch("lofi", "203.0.113.1", norway, start.Add(time.Minute)); n != 1 || total != 1 {
		t.Fatalf("repeat touch = (%d, %d), want (1, 1)", n, total)
	}
	if n, total := listeners.Touch("jazz", "203.0.113.2", geo.Location{}, start.Add(2*time.Minute)); n != 1 || total != 2 {
		t.Fatalf("jazz touch = (%d, %d), want (1, 2)", n, total)
	}

	active := listeners.active("lofi", start.Add(time.Minute+trackListenerWindow-time.Second))
	if len(active) != 1 || active[0].countryCode != "NO" {
		t.Errorf("active before expiry = %#v, want one listener from NO", active)
	}
	if active := listeners.active("lofi", start.Add(time.Minute+trackListenerWindow)); len(active) != 0 {
		t.Errorf("active after expiry = %#v, want none", active)
	}

	later := start.Add(2*time.Minute + trackListenerWindow)
	if n, total := listeners.Touch("lofi", "203.0.113.3", geo.Location{}, later); n != 1 || total != 1 {
		t.Errorf("touch after expiry = (%d, %d), want (1, 1)", n, total)
	}
}

func TestTrackStatisticsResponses(t *testing.T) {
	dir := t.TempDir()
	var tracks []library.Track
	for _, name := range []string{"first", "second", "third"} {
		path := filepath.Join(dir, name+".mp3")
		if err := os.WriteFile(path, []byte("audio data"), 0o600); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, library.Track{Path: path, Title: name})
	}
	db, err := stats.Open(filepath.Join(dir, "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	idx := NewTrackIndex("lofi", "Lo-fi", tracks)
	listeners := NewTrackListeners()
	file := &TrackFile{Index: idx, StatsDB: db, Listeners: listeners}

	for _, play := range []struct {
		ip    string
		track int
	}{
		{"203.0.113.1", 1},
		{"203.0.113.1", 1},
		{"203.0.113.2", 0},
	} {
		id := idx.entries[play.track].ID
		req := httptest.NewRequest(http.MethodGet, "/lofi/tracks/"+id, nil)
		req.SetPathValue("id", id)
		req.Header.Set("X-Forwarded-For", play.ip)
		rec := httptest.NewRecorder()
		file.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("track status = %d, want %d", rec.Code, http.StatusOK)
		}
	}

	t.Run("station", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://radio.example/lofi/tracks/statistics", nil)
		(&TrackStatistics{Index: idx, StatsDB: db, Listeners: listeners}).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"top_countries":[]`) {
			t.Error("response does not return an empty top_countries array")
		}
		var got trackStatsResponse
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if got.TotalPlays != 3 {
			t.Errorf("total plays = %d, want 3", got.TotalPlays)
		}
		if got.ActiveListeners != 2 {
			t.Errorf("active listeners = %d, want 2", got.ActiveListeners)
		}
		if got.PeakListeners != 2 {
			t.Errorf("peak listeners = %d, want 2", got.PeakListeners)
		}
		if len(got.TopTracks) != 2 {
			t.Fatalf("top tracks = %#v, want two played tracks", got.TopTracks)
		}
		if got.TopTracks[0].Title != "second" || got.TopTracks[0].Plays != 2 {
			t.Errorf("first top track = %#v, want second with 2 plays", got.TopTracks[0])
		}
		if want := "https://radio.example/lofi/tracks/" + idx.entries[1].ID; got.TopTracks[0].URL != want {
			t.Errorf("first top track URL = %q, want %q", got.TopTracks[0].URL, want)
		}
		if len(got.Daily) != 1 || got.Daily[0].Plays != 3 {
			t.Errorf("daily = %#v, want one day with 3 plays", got.Daily)
		}
	})

	t.Run("global", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tracks/statistics", nil)
		(&GlobalTrackStatistics{
			Indexes:   map[string]*TrackIndex{"lofi": idx},
			StatsDB:   db,
			Listeners: listeners,
		}).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var got globalTrackStatsResponse
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.TotalPlays != 3 {
			t.Errorf("total plays = %d, want 3", got.TotalPlays)
		}
		if got.PeakListeners != 2 {
			t.Errorf("peak listeners = %d, want 2", got.PeakListeners)
		}
		if got.Stations["lofi"].ActiveListeners != 2 {
			t.Errorf("lofi active listeners = %d, want 2", got.Stations["lofi"].ActiveListeners)
		}
	})
}
