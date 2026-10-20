package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperationalEndpointContracts(t *testing.T) {
	for _, tc := range []struct {
		path, method, body string
		status             int
	}{
		{"/healthz", "GET", `{"status":"alive"}`, 200},
		{"/readyz", "GET", `{"error":"game_worker_unavailable"}`, 503},
		{"/api/v1/capabilities", "GET", `{"game_worker":"unavailable","multiplayer_available":false,"target_max_players_per_room":4}`, 200},
		{"/healthz", "POST", `{"error":"method_not_allowed"}`, 405},
		{"/readyz", "HEAD", `{"error":"method_not_allowed"}`, 405},
		{"/api/v1/capabilities", "PUT", `{"error":"method_not_allowed"}`, 405},
		{"/unknown", "GET", `{"error":"not_found"}`, 404},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var logs bytes.Buffer
			handler := Handler(slog.New(slog.NewJSONHandler(&logs, nil)))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || strings.TrimSpace(response.Body.String()) != tc.body {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing JSON or freshness contract")
			}
			if tc.status == 405 && response.Header().Get("Allow") != "GET" {
				t.Fatal("missing supported method")
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			requestID := response.Header().Get("X-Request-ID")
			if requestID == "" || record["request_id"] != requestID || record["status"] != float64(tc.status) || record["method"] != tc.method || record["component"] != "control" || record["phase"] != "request_completed" {
				t.Fatalf("request response/log correlation missing: %v", record)
			}
		})
	}
}

func TestRequestLogsExcludeSensitiveInputs(t *testing.T) {
	var logs bytes.Buffer
	handler := Handler(slog.New(slog.NewJSONHandler(&logs, nil)))
	ids := make(map[string]bool)
	for range 2 {
		request := httptest.NewRequest("POST", "/private-path-secret?token=query-secret", strings.NewReader("payload-secret"))
		request.Header.Set("Authorization", "Bearer credential-secret")
		request.Header.Set("X-Request-ID", "external-id-secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		id := response.Header().Get("X-Request-ID")
		if id == "" || ids[id] {
			t.Fatal("request IDs must be generated uniquely")
		}
		ids[id] = true
	}
	for _, secret := range []string{"private-path-secret", "query-secret", "payload-secret", "credential-secret", "external-id-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logged sensitive input %q", secret)
		}
	}
	if !strings.Contains(logs.String(), `"route":"unmatched"`) {
		t.Fatal("unmatched routes must use bounded log labels")
	}
}

type failingResponse struct{ header http.Header }

func (w failingResponse) Header() http.Header     { return w.header }
func (failingResponse) WriteHeader(int)           {}
func (failingResponse) Write([]byte) (int, error) { return 0, errors.New("response write failed") }

func TestResponseWriteFailureIsReported(t *testing.T) {
	var logs bytes.Buffer
	Handler(slog.New(slog.NewJSONHandler(&logs, nil))).ServeHTTP(failingResponse{header: make(http.Header)}, httptest.NewRequest("GET", "/healthz", nil))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["level"] != "ERROR" || record["error"] != "response write failed" {
		t.Fatalf("write error lost: %v", record)
	}
}
