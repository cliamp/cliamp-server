package stats

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

const (
	globalPeakScope   = "global"
	stationPeakPrefix = "station:"
)

// PeakTracker records the highest concurrent listener counts seen by a server.
type PeakTracker struct {
	db *DB

	mu             sync.Mutex
	listenerCounts map[string]int
	stationPeaks   map[string]int
	globalPeak     int
}

// NewPeakTracker creates a listener peak tracker backed by db.
func NewPeakTracker(db *DB, stationIDs []string) (*PeakTracker, error) {
	t := &PeakTracker{
		db:             db,
		listenerCounts: make(map[string]int, len(stationIDs)),
		stationPeaks:   make(map[string]int, len(stationIDs)),
	}

	for _, station := range stationIDs {
		peak, err := db.StationPeakListeners(station)
		if err != nil {
			return nil, err
		}
		t.stationPeaks[station] = peak
	}

	globalPeak, err := db.GlobalPeakListeners()
	if err != nil {
		return nil, err
	}
	t.globalPeak = globalPeak
	return t, nil
}

// Change records a listener connection or disconnection.
func (t *PeakTracker) Change(station string, delta int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	current := t.listenerCounts[station] + delta
	if current < 0 {
		return fmt.Errorf("listener count for station %q cannot be negative", station)
	}
	t.listenerCounts[station] = current

	var errs []error
	if current > t.stationPeaks[station] {
		if err := t.db.RecordStationPeak(station, current); err != nil {
			errs = append(errs, err)
		} else {
			t.stationPeaks[station] = current
		}
	}

	total := 0
	for _, count := range t.listenerCounts {
		total += count
	}
	if total > t.globalPeak {
		if err := t.db.RecordGlobalPeak(total); err != nil {
			errs = append(errs, err)
		} else {
			t.globalPeak = total
		}
	}
	return errors.Join(errs...)
}

// StationPeakListeners returns the all-time highest listener count for station.
func (d *DB) StationPeakListeners(station string) (int, error) {
	return d.peakListeners(stationPeakPrefix + station)
}

// GlobalPeakListeners returns the all-time highest listener count across stations.
func (d *DB) GlobalPeakListeners() (int, error) {
	return d.peakListeners(globalPeakScope)
}

func (d *DB) peakListeners(scope string) (int, error) {
	var peak int
	err := d.db.QueryRow(
		`SELECT COALESCE((SELECT peak_listeners FROM listener_peaks WHERE scope = ?), 0)`,
		scope,
	).Scan(&peak)
	return peak, err
}

// RecordStationPeak stores station's peak listener count if it is a new high.
func (d *DB) RecordStationPeak(station string, listeners int) error {
	if err := recordPeak(d.db, stationPeakPrefix+station, listeners); err != nil {
		return err
	}

	d.stationCache.invalidate(station)
	return nil
}

// RecordGlobalPeak stores the global peak listener count if it is a new high.
func (d *DB) RecordGlobalPeak(listeners int) error {
	return recordPeak(d.db, globalPeakScope, listeners)
}

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func recordPeak(ex execer, scope string, listeners int) error {
	_, err := ex.Exec(
		`INSERT INTO listener_peaks (scope, peak_listeners) VALUES (?, ?)
		 ON CONFLICT(scope) DO UPDATE SET peak_listeners = MAX(peak_listeners, excluded.peak_listeners)`,
		scope,
		listeners,
	)
	return err
}

func backfillListenerPeaks(db *sql.DB) error {
	const stationPeaks = `WITH events AS (
		SELECT station, connected_at AS at, 1 AS delta FROM listener_sessions
		UNION ALL
		SELECT station, disconnected_at AS at, -1 AS delta FROM listener_sessions
	), running AS (
		SELECT station, SUM(delta) OVER (
			PARTITION BY station ORDER BY at, delta ROWS UNBOUNDED PRECEDING
		) AS listeners
		FROM events
	)
	INSERT OR IGNORE INTO listener_peaks (scope, peak_listeners)
	SELECT ? || station, MAX(listeners)
	FROM running
	GROUP BY station`
	if _, err := db.Exec(stationPeaks, stationPeakPrefix); err != nil {
		return err
	}

	const globalPeak = `WITH events AS (
		SELECT connected_at AS at, 1 AS delta FROM listener_sessions
		UNION ALL
		SELECT disconnected_at AS at, -1 AS delta FROM listener_sessions
	), running AS (
		SELECT SUM(delta) OVER (ORDER BY at, delta ROWS UNBOUNDED PRECEDING) AS listeners
		FROM events
	)
	INSERT OR IGNORE INTO listener_peaks (scope, peak_listeners)
	SELECT ?, COALESCE(MAX(listeners), 0)
	FROM running`
	_, err := db.Exec(globalPeak, globalPeakScope)
	return err
}
