package stats

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func TestTrackStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before play events were logged has play counters only.
	_, err = legacy.Exec(`CREATE TABLE track_plays (
		station TEXT NOT NULL,
		track_id TEXT NOT NULL,
		plays INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (station, track_id)
	);
	INSERT INTO track_plays (station, track_id, plays) VALUES ('lofi', 'first', 5)`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	for _, play := range []TrackPlay{
		{
			Station: "lofi", TrackID: "first", Country: "Norway", CountryCode: "NO", City: "Oslo",
			PlayedAt: now, Listeners: 1, TotalListeners: 1,
		},
		{
			Station: "lofi", TrackID: "second", Country: "Norway", CountryCode: "NO", City: "Oslo",
			PlayedAt: now, Listeners: 2, TotalListeners: 3,
		},
		{
			Station: "lofi", TrackID: "second",
			PlayedAt: now.Add(-48 * time.Hour), Listeners: 1, TotalListeners: 1,
		},
		{
			Station: "jazz", TrackID: "first", Country: "Sweden", CountryCode: "SE", City: "Stockholm",
			PlayedAt: now, Listeners: 1, TotalListeners: 2,
		},
	} {
		if err := db.RecordTrackPlay(play); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.TrackStats("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalPlays != 8 {
		t.Errorf("TotalPlays = %d, want 8 including the counter from before events", got.TotalPlays)
	}
	if got.PeakListeners != 2 {
		t.Errorf("PeakListeners = %d, want 2", got.PeakListeners)
	}
	if len(got.TopCountries) != 1 || got.TopCountries[0].CountryCode != "NO" || got.TopCountries[0].Plays != 2 {
		t.Errorf("TopCountries = %#v, want Norway with 2 plays", got.TopCountries)
	}
	if len(got.TopCities) != 1 || got.TopCities[0].City != "Oslo" || got.TopCities[0].Plays != 2 {
		t.Errorf("TopCities = %#v, want Oslo with 2 plays", got.TopCities)
	}
	if len(got.Daily) != 2 {
		t.Errorf("Daily = %#v, want two days", got.Daily)
	}

	globalPeak, err := db.GlobalTrackPeakListeners()
	if err != nil {
		t.Fatal(err)
	}
	if globalPeak != 3 {
		t.Errorf("global track peak listeners = %d, want 3", globalPeak)
	}

	// Track peaks must not change the radio listener peaks.
	radioPeak, err := db.StationPeakListeners("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if radioPeak != 0 {
		t.Errorf("radio peak listeners = %d, want 0", radioPeak)
	}

	all, err := db.AllTrackStats([]string{"lofi", "jazz"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["jazz"].TotalPlays != 1 {
		t.Errorf("AllTrackStats() = %#v, want lofi and jazz", all)
	}
}

func TestRecordTrackPlayInvalidatesTrackStatsCache(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	play := TrackPlay{Station: "lofi", TrackID: "first", PlayedAt: time.Now()}
	if err := db.RecordTrackPlay(play); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TrackStats("lofi"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordTrackPlay(play); err != nil {
		t.Fatal(err)
	}

	got, err := db.TrackStats("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalPlays != 2 {
		t.Errorf("TotalPlays = %d, want 2 after cache invalidation", got.TotalPlays)
	}
}
