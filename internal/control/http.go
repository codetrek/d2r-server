package control

import (
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
)

type capabilities struct {
	GameWorker              string `json:"game_worker"`
	MultiplayerAvailable    bool   `json:"multiplayer_available"`
	TargetMaxPlayersPerRoom int    `json:"target_max_players_per_room"`
}

// Handler reports operational liveness separately from game readiness. The
// control process cannot become game-ready until a worker is implemented.
func Handler(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := rand.Text()
		writer.Header().Set("X-Request-ID", requestID)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		route, status := "unmatched", http.StatusNotFound
		var body any = map[string]string{"error": "not_found"}
		switch request.URL.Path {
		case "/healthz":
			route, status, body = "/healthz", http.StatusOK, map[string]string{"status": "alive"}
		case "/readyz":
			route, status, body = "/readyz", http.StatusServiceUnavailable, map[string]string{"error": "game_worker_unavailable"}
		case "/api/v1/capabilities":
			route, status, body = "/api/v1/capabilities", http.StatusOK, capabilities{
				GameWorker: "unavailable", MultiplayerAvailable: false, TargetMaxPlayersPerRoom: 4,
			}
		}
		if route != "unmatched" && request.Method != http.MethodGet {
			status, body = http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"}
			writer.Header().Set("Allow", http.MethodGet)
		}
		writer.WriteHeader(status)
		err := json.NewEncoder(writer).Encode(body)
		attributes := []any{"component", "control", "phase", "request_completed", "request_id", requestID, "method", request.Method, "route", route, "status", status}
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelError
			attributes = append(attributes, "error", err)
		}
		logger.Log(request.Context(), level, "http_request", attributes...)
	})
}
