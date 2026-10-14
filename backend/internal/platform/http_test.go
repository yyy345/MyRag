package platform

import (
	"net/http/httptest"
	"testing"
)

func TestScaffoldDoesNotClaimBusinessReadiness(t *testing.T) {
	for path, status := range map[string]int{"/health/live": 200, "/health/ready": 503, "/api/knowledge-bases": 404, "/missing": 404} {
		r := httptest.NewRecorder()
		Handler().ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != status {
			t.Errorf("%s: got %d, want %d", path, r.Code, status)
		}
	}
}
