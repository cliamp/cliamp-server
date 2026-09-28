package stats

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func TestOpenCreatesStatisticsIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE listener_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		station TEXT NOT NULL,
		country TEXT NOT NULL DEFAULT '',
		country_code TEXT NOT NULL DEFAULT '',
		city TEXT NOT NULL DEFAULT '',
		latitude REAL NOT NULL DEFAULT 0,
		longitude REAL NOT NULL DEFAULT 0,
		connected_at TEXT NOT NULL,
		disconnected_at TEXT NOT NULL,
		duration_seconds INTEGER NOT NULL
	)`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO listener_sessions (station, connected_at, disconnected_at, duration_seconds) VALUES ('lofi', '2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z', 3600)`); err != nil {
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

	rows, err := db.db.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_listener_sessions_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	want := []string{
		"idx_listener_sessions_station_city",
		"idx_listener_sessions_station_connected_at",
		"idx_listener_sessions_station_country",
	}
	for _, name := range want {
		if !rows.Next() {
			t.Fatalf("missing index %q", name)
		}
		var got string
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != name {
			t.Errorf("index = %q, want %q", got, name)
		}
	}
	if rows.Next() {
		t.Error("found unexpected index")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM listener_sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("session count = %d, want 1", count)
	}
}

func TestStationStats(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	for _, session := range []Session{
		{
			Station: "lofi", Country: "Norway", CountryCode: "NO", City: "Oslo",
			ConnectedAt: now.Add(-time.Hour), DisconnectedAt: now, DurationSeconds: 3600,
		},
		{
			Station: "lofi", Country: "Norway", CountryCode: "NO", City: "Oslo",
			ConnectedAt: now.Add(-2 * time.Hour), DisconnectedAt: now.Add(-time.Hour), DurationSeconds: 1800,
		},
		{
			Station: "lofi", ConnectedAt: now.Add(-48 * time.Hour), DisconnectedAt: now.Add(-47 * time.Hour), DurationSeconds: 600,
		},
		{
			Station: "old", Country: "Sweden", CountryCode: "SE", City: "Stockholm",
			ConnectedAt: now.Add(-time.Hour), DisconnectedAt: now, DurationSeconds: 3600,
		},
	} {
		if err := db.Record(session); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.StationStats("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalSessions != 3 {
		t.Errorf("TotalSessions = %d, want 3", got.TotalSessions)
	}
	if got.TotalListenHours != 1.7 {
		t.Errorf("TotalListenHours = %v, want 1.7", got.TotalListenHours)
	}
	if len(got.TopCountries) != 1 || got.TopCountries[0].CountryCode != "NO" || got.TopCountries[0].Sessions != 2 {
		t.Errorf("TopCountries = %#v, want Norway with 2 sessions", got.TopCountries)
	}
	if len(got.TopCities) != 1 || got.TopCities[0].City != "Oslo" || got.TopCities[0].Sessions != 2 {
		t.Errorf("TopCities = %#v, want Oslo with 2 sessions", got.TopCities)
	}
	if len(got.Daily) != 2 {
		t.Errorf("Daily = %#v, want two days", got.Daily)
	}

	all, err := db.AllStats([]string{"lofi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all["lofi"].TotalSessions != 3 {
		t.Errorf("AllStats() = %#v, want lofi only", all)
	}
}

func TestRecordInvalidatesStationStatsCache(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	first := Session{
		Station: "lofi", ConnectedAt: now.Add(-time.Hour), DisconnectedAt: now, DurationSeconds: 3600,
	}
	if err := db.Record(first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StationStats("lofi"); err != nil {
		t.Fatal(err)
	}

	second := first
	second.ConnectedAt = now.Add(-2 * time.Hour)
	second.DisconnectedAt = now.Add(-time.Hour)
	if err := db.Record(second); err != nil {
		t.Fatal(err)
	}

	got, err := db.StationStats("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalSessions != 2 {
		t.Errorf("TotalSessions = %d, want 2 after cache invalidation", got.TotalSessions)
	}
}

func TestTrackPlayCounts(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, play := range []struct {
		station string
		trackID string
	}{
		{station: "lofi", trackID: "first"},
		{station: "lofi", trackID: "first"},
		{station: "lofi", trackID: "second"},
		{station: "jazz", trackID: "first"},
	} {
		if err := db.RecordTrackPlay(TrackPlay{Station: play.station, TrackID: play.trackID, PlayedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.TrackPlayCounts("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if got["first"] != 2 {
		t.Errorf("first plays = %d, want 2", got["first"])
	}
	if got["second"] != 1 {
		t.Errorf("second plays = %d, want 1", got["second"])
	}
	if len(got) != 2 {
		t.Errorf("TrackPlayCounts() returned %d tracks, want 2", len(got))
	}
}

func TestOpenBackfillsListenerPeaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE listener_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		station TEXT NOT NULL,
		country TEXT NOT NULL DEFAULT '',
		country_code TEXT NOT NULL DEFAULT '',
		city TEXT NOT NULL DEFAULT '',
		latitude REAL NOT NULL DEFAULT 0,
		longitude REAL NOT NULL DEFAULT 0,
		connected_at TEXT NOT NULL,
		disconnected_at TEXT NOT NULL,
		duration_seconds INTEGER NOT NULL
	)`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	for _, session := range []struct {
		station      string
		connectedAt  string
		disconnected string
	}{
		{"lofi", "2026-01-01T01:00:00Z", "2026-01-01T03:00:00Z"},
		{"lofi", "2026-01-01T02:00:00Z", "2026-01-01T04:00:00Z"},
		{"jazz", "2026-01-01T02:30:00Z", "2026-01-01T03:30:00Z"},
	} {
		if _, err := legacy.Exec(
			`INSERT INTO listener_sessions (station, connected_at, disconnected_at, duration_seconds) VALUES (?, ?, ?, 3600)`,
			session.station,
			session.connectedAt,
			session.disconnected,
		); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	lofiPeak, err := db.StationPeakListeners("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if lofiPeak != 2 {
		t.Errorf("lofi peak listeners = %d, want 2", lofiPeak)
	}
	globalPeak, err := db.GlobalPeakListeners()
	if err != nil {
		t.Fatal(err)
	}
	if globalPeak != 3 {
		t.Errorf("global peak listeners = %d, want 3", globalPeak)
	}
}

func TestPeakTrackerPersistsListenerHighs(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tracker, err := NewPeakTracker(db, []string{"lofi", "jazz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		station string
		delta   int
	}{
		{"lofi", 1},
		{"jazz", 1},
		{"lofi", -1},
		{"jazz", -1},
	} {
		if err := tracker.Change(change.station, change.delta); err != nil {
			t.Fatal(err)
		}
	}

	lofiPeak, err := db.StationPeakListeners("lofi")
	if err != nil {
		t.Fatal(err)
	}
	if lofiPeak != 1 {
		t.Errorf("lofi peak listeners = %d, want 1", lofiPeak)
	}
	globalPeak, err := db.GlobalPeakListeners()
	if err != nil {
		t.Fatal(err)
	}
	if globalPeak != 2 {
		t.Errorf("global peak listeners = %d, want 2", globalPeak)
	}
}
