package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStationsMakesURLsAbsolute(t *testing.T) {
	h := &Stations{Entries: []StationEntry{
		{ID: "lofi", Name: "Lofi", Stream: "/lofi/stream"},
		{ID: "omarchy", Name: "Omarchy", Stream: "/omarchy/stream", Tracks: 33, TracksURL: "/omarchy/tracks"},
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://radio.example/stations", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got struct {
		Stations []StationEntry `json:"stations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := []StationEntry{
		{ID: "lofi", Name: "Lofi", Stream: "https://radio.example/lofi/stream"},
		{ID: "omarchy", Name: "Omarchy", Stream: "https://radio.example/omarchy/stream", Tracks: 33, TracksURL: "https://radio.example/omarchy/tracks"},
	}
	if len(got.Stations) != len(want) {
		t.Fatalf("stations = %#v, want %#v", got.Stations, want)
	}
	for i := range want {
		if got.Stations[i] != want[i] {
			t.Errorf("station %d = %#v, want %#v", i, got.Stations[i], want[i])
		}
	}
	if h.Entries[0].Stream != "/lofi/stream" {
		t.Error("handler changed its stored entries")
	}
}
