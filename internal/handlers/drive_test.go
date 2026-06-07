package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDriveRemoteAPIBase(t *testing.T) {
	tests := map[string]string{
		"https://drive.example.com":      "https://drive.example.com/api",
		"https://drive.example.com/api":  "https://drive.example.com/api",
		"https://drive.example.com/root": "https://drive.example.com/root/api",
	}
	for input, want := range tests {
		if got := driveRemoteAPIBase(input); got != want {
			t.Fatalf("driveRemoteAPIBase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDriveRequestOriginUsesForwardedPublicAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/drive/remotes", nil)
	context.Request.Header.Set("X-Forwarded-Proto", "https")
	context.Request.Header.Set("X-Forwarded-Host", "drive.example.com")

	origin, err := driveRequestOrigin(context)
	if err != nil {
		t.Fatalf("driveRequestOrigin: %v", err)
	}
	if origin != "https://drive.example.com" {
		t.Fatalf("origin = %q, want https://drive.example.com", origin)
	}
}

func TestRegisterReciprocalDrive(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drive/remotes" {
			t.Fatalf("path = %q, want /api/drive/remotes", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer remote-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Drive-Reciprocal") != "1" {
			t.Fatalf("reciprocal header = %q", r.Header.Get("X-Drive-Reciprocal"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	err := registerReciprocalDrive(server.URL, "remote-token", "local-drive", "https://local.example.com", "local-token")
	if err != nil {
		t.Fatalf("registerReciprocalDrive: %v", err)
	}
	if received["name"] != "local-drive" || received["url"] != "https://local.example.com" || received["token"] != "local-token" {
		t.Fatalf("received payload = %#v", received)
	}
}
