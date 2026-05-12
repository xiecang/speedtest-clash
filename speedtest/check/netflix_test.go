package check

import (
	"net/http"
	"testing"
)

func TestParseNetflixFastCountry(t *testing.T) {
	t.Parallel()

	body := []byte(`{"targets":[{"location":{"country":"JP"}}]}`)
	country, ok := parseNetflixFastCountry(body)
	if !ok {
		t.Fatal("parseNetflixFastCountry returned false")
	}
	if country != "JP" {
		t.Fatalf("country = %q, want JP", country)
	}
}

func TestClassifyNetflixTitleStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status1 int
		status2 int
		ok      bool
	}{
		{name: "full unlock on 200", status1: http.StatusOK, status2: http.StatusNotFound, ok: true},
		{name: "full unlock on redirect", status1: http.StatusMovedPermanently, status2: http.StatusNotFound, ok: true},
		{name: "originals only", status1: http.StatusNotFound, status2: http.StatusNotFound, ok: false},
		{name: "banned", status1: http.StatusForbidden, status2: http.StatusOK, ok: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ok := classifyNetflixTitleStatus(tt.status1, tt.status2)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
		})
	}
}
