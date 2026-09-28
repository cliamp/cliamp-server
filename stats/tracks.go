package stats

import (
	"database/sql"
	"time"
)

const (
	trackGlobalPeakScope   = "tracks:global"
	trackStationPeakPrefix = "tracks:station:"
)

// TrackPlay holds the data recorded when a client starts an exposed track.
type TrackPlay struct {
	Station     string
	TrackID     string
	Country     string
	CountryCode string
	City        string
	Latitude    float64
	Longitude   float64
	PlayedAt    time.Time

	// Listeners and TotalListeners hold the active track listener counts for
	// the station and for all stations. Both counts include this play.
	Listeners      int
	TotalListeners int
}

// TrackStatsResult holds aggregated play statistics for the exposed tracks of
// a single station.
type TrackStatsResult struct {
	TotalPlays    int64               `json:"total_plays"`
	PeakListeners int                 `json:"peak_listeners"`
	TopCountries  []TrackCountryStats `json:"top_countries"`
	TopCities     []TrackCityStats    `json:"top_cities"`
	Daily         []TrackDailyStats   `json:"daily"`
}

// TrackCountryStats holds the track plays from a country.
type TrackCountryStats struct {
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Plays       int64  `json:"plays"`
}

// TrackCityStats holds the track plays from a city.
type TrackCityStats struct {
	City        string `json:"city"`
	CountryCode string `json:"country_code"`
	Plays       int64  `json:"plays"`
}

// TrackDailyStats holds the track plays for a single day.
type TrackDailyStats struct {
	Date  string `json:"date"`
	Plays int64  `json:"plays"`
}

func createTrackPlaySchema(db *sql.DB) error {
	const schema = `
		CREATE TABLE IF NOT EXISTS track_plays (
			station TEXT    NOT NULL,
			track_id TEXT   NOT NULL,
			plays    INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (station, track_id)
		);

		CREATE TABLE IF NOT EXISTS track_play_events (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			station      TEXT    NOT NULL,
			track_id     TEXT    NOT NULL,
			country      TEXT    NOT NULL DEFAULT '',
			country_code TEXT    NOT NULL DEFAULT '',
			city         TEXT    NOT NULL DEFAULT '',
			latitude     REAL    NOT NULL DEFAULT 0,
			longitude    REAL    NOT NULL DEFAULT 0,
			played_at    TEXT    NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_track_play_events_station_played_at
		ON track_play_events (station, played_at);

		CREATE INDEX IF NOT EXISTS idx_track_play_events_station_country
		ON track_play_events (station, country, country_code)
		WHERE country != '';

		CREATE INDEX IF NOT EXISTS idx_track_play_events_station_city
		ON track_play_events (station, city, country_code)
		WHERE city != '';`
	_, err := db.Exec(schema)
	return err
}

// RecordTrackPlay stores one play of an exposed track. It increments the play
// count of the track, logs the play for aggregated statistics, and raises the
// track listener peaks when the play sets a new high.
func (d *DB) RecordTrackPlay(p TrackPlay) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO track_plays (station, track_id, plays) VALUES (?, ?, 1)
		 ON CONFLICT(station, track_id) DO UPDATE SET plays = plays + 1`,
		p.Station,
		p.TrackID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO track_play_events
			(station, track_id, country, country_code, city, latitude, longitude, played_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Station,
		p.TrackID,
		p.Country,
		p.CountryCode,
		p.City,
		p.Latitude,
		p.Longitude,
		p.PlayedAt.UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	if err := recordPeak(tx, trackStationPeakPrefix+p.Station, p.Listeners); err != nil {
		return err
	}
	if err := recordPeak(tx, trackGlobalPeakScope, p.TotalListeners); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	d.trackCache.invalidate(p.Station)
	return nil
}

// TrackPlayCounts returns all recorded track play counts for a station.
func (d *DB) TrackPlayCounts(station string) (map[string]int64, error) {
	rows, err := d.db.Query(
		`SELECT track_id, plays FROM track_plays WHERE station = ?`,
		station,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int64)
	for rows.Next() {
		var trackID string
		var plays int64
		if err := rows.Scan(&trackID, &plays); err != nil {
			return nil, err
		}
		counts[trackID] = plays
	}
	return counts, rows.Err()
}

// TrackPeakListeners returns the all-time highest track listener count for
// station.
func (d *DB) TrackPeakListeners(station string) (int, error) {
	return d.peakListeners(trackStationPeakPrefix + station)
}

// GlobalTrackPeakListeners returns the all-time highest track listener count
// across stations.
func (d *DB) GlobalTrackPeakListeners() (int, error) {
	return d.peakListeners(trackGlobalPeakScope)
}

// TrackStats returns aggregated play statistics for the exposed tracks of a
// single station.
func (d *DB) TrackStats(station string) (*TrackStatsResult, error) {
	return d.trackCache.get(station, func() (*TrackStatsResult, error) {
		return d.trackStats(station)
	})
}

// AllTrackStats returns aggregated play statistics for stationIDs, keyed by
// station ID.
func (d *DB) AllTrackStats(stationIDs []string) (map[string]*TrackStatsResult, error) {
	result := make(map[string]*TrackStatsResult, len(stationIDs))
	for _, id := range stationIDs {
		s, err := d.TrackStats(id)
		if err != nil {
			return nil, err
		}
		result[id] = s
	}
	return result, nil
}

func (d *DB) trackStats(station string) (*TrackStatsResult, error) {
	result := &TrackStatsResult{}

	// The play counters include plays from before play events were logged,
	// so the total comes from the counters and not from the event log.
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(plays), 0) FROM track_plays WHERE station = ?`, station,
	).Scan(&result.TotalPlays)
	if err != nil {
		return nil, err
	}
	result.PeakListeners, err = d.TrackPeakListeners(station)
	if err != nil {
		return nil, err
	}

	result.TopCountries, err = d.topTrackCountries(station)
	if err != nil {
		return nil, err
	}
	result.TopCities, err = d.topTrackCities(station)
	if err != nil {
		return nil, err
	}
	result.Daily, err = d.dailyTrackPlays(station)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func cloneTrackStats(in *TrackStatsResult) *TrackStatsResult {
	if in == nil {
		return nil
	}
	return &TrackStatsResult{
		TotalPlays:    in.TotalPlays,
		PeakListeners: in.PeakListeners,
		TopCountries:  append([]TrackCountryStats(nil), in.TopCountries...),
		TopCities:     append([]TrackCityStats(nil), in.TopCities...),
		Daily:         append([]TrackDailyStats(nil), in.Daily...),
	}
}

func (d *DB) topTrackCountries(station string) ([]TrackCountryStats, error) {
	rows, err := d.db.Query(
		`SELECT country, country_code, COUNT(*) AS plays
		 FROM track_play_events
		 WHERE station = ? AND country != ''
		 GROUP BY country, country_code
		 ORDER BY plays DESC
		 LIMIT 10`, station,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrackCountryStats
	for rows.Next() {
		var c TrackCountryStats
		if err := rows.Scan(&c.Country, &c.CountryCode, &c.Plays); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) topTrackCities(station string) ([]TrackCityStats, error) {
	rows, err := d.db.Query(
		`SELECT city, country_code, COUNT(*) AS plays
		 FROM track_play_events
		 WHERE station = ? AND city != ''
		 GROUP BY city, country_code
		 ORDER BY plays DESC
		 LIMIT 10`, station,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrackCityStats
	for rows.Next() {
		var c TrackCityStats
		if err := rows.Scan(&c.City, &c.CountryCode, &c.Plays); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) dailyTrackPlays(station string) ([]TrackDailyStats, error) {
	rows, err := d.db.Query(
		`SELECT DATE(played_at) AS day, COUNT(*) AS plays
		 FROM track_play_events
		 WHERE station = ?
		   AND played_at >= DATE('now', '-30 days')
		 GROUP BY day
		 ORDER BY day`, station,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrackDailyStats
	for rows.Next() {
		var d TrackDailyStats
		if err := rows.Scan(&d.Date, &d.Plays); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
