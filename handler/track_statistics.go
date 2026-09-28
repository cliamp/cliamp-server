package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"cliamp-server/geo"
	"cliamp-server/stats"
)

// trackListenerWindow is how long a client counts as an active track listener
// after it starts a track. The server sees file requests, not playback, and
// players usually fetch a whole file in seconds. The window covers one or two
// typical songs, so a client that plays a playlist stays active.
const trackListenerWindow = 10 * time.Minute

// trackRepeatWindow is how long a new request for the same track from the
// same client counts as the same play. Players often send two requests to
// start one track. For example, cliamp probes the URL and then downloads the
// file.
const trackRepeatWindow = 30 * time.Second

// topTrackLimit is the number of tracks in a top_tracks list.
const topTrackLimit = 10

// TrackListeners tracks the clients that started an exposed track within
// trackListenerWindow. IP addresses stay in memory and are never stored.
type TrackListeners struct {
	mu       sync.Mutex
	stations map[string]map[string]trackListener // station ID -> IP -> listener
}

type trackListener struct {
	lastPlay    time.Time
	trackID     string
	country     string
	countryCode string
}

// NewTrackListeners creates an empty track listener tracker.
func NewTrackListeners() *TrackListeners {
	return &TrackListeners{stations: make(map[string]map[string]trackListener)}
}

// Touch marks ip as an active listener of station at now, playing trackID.
// It returns the active listener count for station and the total for all
// stations. repeat is true when the same client requested the same track
// within trackRepeatWindow, so the request is not a new play.
func (t *TrackListeners) Touch(station, ip, trackID string, loc geo.Location, now time.Time) (listeners, total int, repeat bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	clients := t.stations[station]
	if clients == nil {
		clients = make(map[string]trackListener)
		t.stations[station] = clients
	}
	if prev, ok := clients[ip]; ok && prev.trackID == trackID && now.Sub(prev.lastPlay) < trackRepeatWindow {
		repeat = true
	}
	clients[ip] = trackListener{
		lastPlay:    now,
		trackID:     trackID,
		country:     loc.Country,
		countryCode: loc.CountryCode,
	}

	for id := range t.stations {
		n := t.prune(id, now)
		if id == station {
			listeners = n
		}
		total += n
	}
	return listeners, total, repeat
}

// active returns the active listeners of station at now.
func (t *TrackListeners) active(station string, now time.Time) []trackListener {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.prune(station, now)
	out := make([]trackListener, 0, len(t.stations[station]))
	for _, l := range t.stations[station] {
		out = append(out, l)
	}
	return out
}

// prune removes the expired listeners of station and returns how many remain.
// The caller must hold t.mu.
func (t *TrackListeners) prune(station string, now time.Time) int {
	clients := t.stations[station]
	for ip, l := range clients {
		if now.Sub(l.lastPlay) >= trackListenerWindow {
			delete(clients, ip)
		}
	}
	if len(clients) == 0 {
		delete(t.stations, station)
	}
	return len(clients)
}

// TrackListenerCountry holds the active track listeners from a country.
type TrackListenerCountry struct {
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Listeners   int    `json:"listeners"`
}

type trackStatsResponse struct {
	TotalPlays              int64                     `json:"total_plays"`
	PeakListeners           int                       `json:"peak_listeners"`
	ActiveListeners         int                       `json:"active_listeners"`
	ActiveListenerCountries []TrackListenerCountry    `json:"active_listener_countries"`
	TopCountries            []stats.TrackCountryStats `json:"top_countries"`
	TopCities               []stats.TrackCityStats    `json:"top_cities"`
	TopTracks               []TrackEntry              `json:"top_tracks"`
	Daily                   []stats.TrackDailyStats   `json:"daily"`
}

// TrackStatistics handles GET /{station}/tracks/statistics - public play
// statistics for the exposed tracks of one station.
type TrackStatistics struct {
	Index     *TrackIndex
	StatsDB   *stats.DB
	Listeners *TrackListeners
}

func (h *TrackStatistics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	result, err := h.StatsDB.TrackStats(h.Index.StationID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp, err := trackStatsPayload(r, h.Index, h.StatsDB, h.Listeners, result)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GlobalTrackStatistics handles GET /tracks/statistics - public play
// statistics for the exposed tracks of all stations.
type GlobalTrackStatistics struct {
	Indexes   map[string]*TrackIndex // station ID -> exposed tracks
	StatsDB   *stats.DB
	Listeners *TrackListeners
}

type globalTrackStatsResponse struct {
	TotalPlays    int64                         `json:"total_plays"`
	PeakListeners int                           `json:"peak_listeners"`
	Stations      map[string]trackStatsResponse `json:"stations"`
}

func (g *GlobalTrackStatistics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	stationIDs := make([]string, 0, len(g.Indexes))
	for id := range g.Indexes {
		stationIDs = append(stationIDs, id)
	}
	allStats, err := g.StatsDB.AllTrackStats(stationIDs)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	peakListeners, err := g.StatsDB.GlobalTrackPeakListeners()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := globalTrackStatsResponse{
		PeakListeners: peakListeners,
		Stations:      make(map[string]trackStatsResponse, len(g.Indexes)),
	}
	for id, idx := range g.Indexes {
		payload, err := trackStatsPayload(r, idx, g.StatsDB, g.Listeners, allStats[id])
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		resp.TotalPlays += payload.TotalPlays
		resp.Stations[id] = payload
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// trackStatsPayload combines the stored statistics of one station with its
// active listeners and its most played tracks.
func trackStatsPayload(r *http.Request, idx *TrackIndex, db *stats.DB, listeners *TrackListeners, result *stats.TrackStatsResult) (trackStatsResponse, error) {
	if result == nil {
		result = &stats.TrackStatsResult{}
	}
	playCounts, err := db.TrackPlayCounts(idx.StationID)
	if err != nil {
		return trackStatsResponse{}, err
	}

	var active []trackListener
	if listeners != nil {
		active = listeners.active(idx.StationID, time.Now())
	}

	resp := trackStatsResponse{
		TotalPlays:              result.TotalPlays,
		PeakListeners:           result.PeakListeners,
		ActiveListeners:         len(active),
		ActiveListenerCountries: activeTrackListenerCountries(active),
		TopCountries:            result.TopCountries,
		TopCities:               result.TopCities,
		TopTracks:               topTracks(idx.withURLs(r, playCounts)),
		Daily:                   result.Daily,
	}

	// Return empty slices instead of null in JSON.
	if resp.TopCountries == nil {
		resp.TopCountries = []stats.TrackCountryStats{}
	}
	if resp.TopCities == nil {
		resp.TopCities = []stats.TrackCityStats{}
	}
	if resp.Daily == nil {
		resp.Daily = []stats.TrackDailyStats{}
	}
	return resp, nil
}

// topTracks returns the most played entries, highest play count first.
// Tracks that were never played are left out.
func topTracks(entries []TrackEntry) []TrackEntry {
	out := make([]TrackEntry, 0, topTrackLimit)
	for _, e := range entries {
		if e.Plays > 0 {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Plays > out[j].Plays
	})
	if len(out) > topTrackLimit {
		out = out[:topTrackLimit]
	}
	return out
}

// activeTrackListenerCountries counts the active track listeners by country,
// highest count first.
func activeTrackListenerCountries(active []trackListener) []TrackListenerCountry {
	byCode := make(map[string]*TrackListenerCountry)
	for _, l := range active {
		if l.country == "" {
			continue
		}
		c, ok := byCode[l.countryCode]
		if !ok {
			c = &TrackListenerCountry{Country: l.country, CountryCode: l.countryCode}
			byCode[l.countryCode] = c
		}
		c.Listeners++
	}

	out := make([]TrackListenerCountry, 0, len(byCode))
	for _, c := range byCode {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Listeners != out[j].Listeners {
			return out[i].Listeners > out[j].Listeners
		}
		return out[i].CountryCode < out[j].CountryCode
	})
	return out
}
