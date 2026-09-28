package server

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"cliamp-server/broadcast"
	"cliamp-server/config"
	"cliamp-server/geo"
	"cliamp-server/handler"
	"cliamp-server/library"
	"cliamp-server/stats"
)

//go:embed logo.svg
var assets embed.FS

// CORS policy. Every route this server exposes is read-only and
// unauthenticated, so a wildcard origin is safe. Access-Control-Allow-Credentials
// is deliberately never set: pairing it with "*" is what turns a public read
// API into a credential-leaking one, and browsers reject the combination.
const (
	corsMethods = "GET, HEAD, OPTIONS"
	corsHeaders = "Range, Icy-MetaData"
	// Browsers hide response headers from cross-origin JavaScript unless the
	// server names them here. Content-Range is what lets a web player seek.
	corsExpose = "Content-Length, Content-Range, Accept-Ranges, " +
		"Icy-Name, Icy-Genre, Icy-Br, Icy-Sr, Icy-Metaint, Icy-Pub, Icy-Url, Icy-Logo"
	corsMaxAge = "86400"
)

// withCORS answers cross-origin preflights and tags every response so browser
// clients can read the API, the playlists and the audio itself.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", corsExpose)

		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", corsMethods)
			h.Set("Access-Control-Allow-Headers", corsHeaders)
			h.Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Station holds the per-station runtime state.
type Station struct {
	Hub        *broadcast.Hub
	Config     config.StationConfig
	TrackCount int
	Tracks     []library.Track
}

// Server wraps the HTTP server and route configuration.
type Server struct {
	httpServer *http.Server
	cfg        *config.Config
	startTime  time.Time
}

// New creates a new server with all routes configured for multiple stations.
// geoDB may be nil if no MaxMind database is configured.
// statsDB may be nil if statistics are not enabled.
func New(cfg *config.Config, stations map[string]*Station, geoDB *geo.DB, statsDB *stats.DB) *Server {
	s := &Server{
		cfg:       cfg,
		startTime: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+handler.LogoPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFileFS(w, r, assets, "logo.svg")
	})

	// Track listeners are shared so the global track peak covers all stations.
	trackListeners := handler.NewTrackListeners()
	trackIndexes := make(map[string]*handler.TrackIndex)

	// Per-station routes
	for id, st := range stations {
		prefix := "/" + id

		mux.Handle(prefix+"/stream", &handler.Stream{
			Hub:          st.Hub,
			StationID:    id,
			MetaInt:      cfg.Stream.MetaInt,
			Name:         st.Config.Name,
			Genre:        st.Config.Genre,
			URL:          st.Config.URL,
			IntroFile:    st.Config.IntroFile,
			IntroSeenTTL: time.Duration(cfg.Stream.IntroSeenTTL) * time.Hour,
			GeoDB:        geoDB,
		})

		mux.Handle(prefix+"/stream.pls", &handler.PlaylistPLS{
			Name:   st.Config.Name,
			Prefix: id,
		})

		mux.Handle(prefix+"/stream.m3u", &handler.PlaylistM3U{
			Name:   st.Config.Name,
			Prefix: id,
		})

		mux.Handle(prefix+"/status", &handler.Status{
			Hub:         st.Hub,
			StartTime:   s.startTime,
			Password:    cfg.Admin.Password,
			TrackCount:  st.TrackCount,
			StationName: st.Config.Name,
		})

		if statsDB != nil {
			mux.Handle(prefix+"/statistics", &handler.Statistics{
				Hub:     st.Hub,
				StatsDB: statsDB,
				Station: id,
			})
		}

		if st.Config.ExposeTracks {
			idx := handler.NewTrackIndex(id, st.Config.Name, st.Tracks)
			trackIndexes[id] = idx

			mux.Handle(prefix+"/tracks", &handler.TracksJSON{Index: idx, StatsDB: statsDB})
			mux.Handle(prefix+"/tracks.m3u", &handler.TracksM3U{Index: idx})
			mux.Handle("GET "+prefix+"/tracks/{id}", &handler.TrackFile{
				Index:     idx,
				StatsDB:   statsDB,
				GeoDB:     geoDB,
				Listeners: trackListeners,
			})
			if statsDB != nil {
				mux.Handle("GET "+prefix+"/tracks/statistics", &handler.TrackStatistics{
					Index:     idx,
					StatsDB:   statsDB,
					Listeners: trackListeners,
				})
			}

			slog.Info("track listing exposed",
				"station", id,
				"tracks", idx.Len(),
				"prefix", prefix+"/tracks",
			)
		}

		slog.Info("station registered",
			"id", id,
			"name", st.Config.Name,
			"tracks", st.TrackCount,
			"prefix", prefix,
		)
	}

	// Global status route
	stationInfos := make(map[string]*handler.StationInfo, len(stations))
	for id, st := range stations {
		stationInfos[id] = &handler.StationInfo{
			Hub:        st.Hub,
			Name:       st.Config.Name,
			TrackCount: st.TrackCount,
		}
	}

	// Global playlist with all stations, ordered by config file order.
	plsStations := make([]handler.PLSStation, 0, len(stations))
	for _, id := range cfg.StationOrder {
		st := stations[id]
		plsStations = append(plsStations, handler.PLSStation{
			Name:   st.Config.Name,
			Prefix: id,
		})
	}
	mux.Handle("/streams.pls", &handler.GlobalPlaylistPLS{Stations: plsStations})

	m3uStations := make([]handler.M3UStation, 0, len(stations))
	for _, id := range cfg.StationOrder {
		st := stations[id]
		m3uStations = append(m3uStations, handler.M3UStation{
			Name:   st.Config.Name,
			Prefix: id,
		})
	}
	mux.Handle("/streams.m3u", &handler.GlobalPlaylistM3U{Stations: m3uStations})

	mux.Handle("/status", &handler.GlobalStatus{
		Stations:  stationInfos,
		StartTime: s.startTime,
		Password:  cfg.Admin.Password,
	})

	if statsDB != nil {
		statsInfos := make(map[string]*handler.StationStatsInfo, len(stations))
		for id, st := range stations {
			statsInfos[id] = &handler.StationStatsInfo{
				Hub: st.Hub,
			}
		}

		mux.Handle("/statistics", &handler.GlobalStatistics{
			Stations: statsInfos,
			StatsDB:  statsDB,
		})

		if len(trackIndexes) > 0 {
			mux.Handle("GET /tracks/statistics", &handler.GlobalTrackStatistics{
				Indexes:   trackIndexes,
				StatsDB:   statsDB,
				Listeners: trackListeners,
			})
		}
	}

	s.httpServer = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s
}

// ListenAndServe starts the HTTP server with TCP_NODELAY enabled
// so each Flush sends a TCP packet immediately instead of being
// batched by Nagle's algorithm.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	slog.Info("server starting", "addr", s.httpServer.Addr)
	return s.httpServer.Serve(&noDelayListener{ln})
}

// noDelayListener wraps a net.Listener and sets TCP_NODELAY on every
// accepted connection.  For a ~128 kbps audio stream the individual
// writes are small (~400 B); without TCP_NODELAY, Nagle's algorithm
// can delay them by up to 200 ms while waiting for ACKs.
type noDelayListener struct {
	net.Listener
}

func (l *noDelayListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return c, nil
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	slog.Info("server shutting down")
	return s.httpServer.Shutdown(ctx)
}
