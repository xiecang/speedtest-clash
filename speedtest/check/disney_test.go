package check

import "testing"

func TestParseDisneyTokenResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		refresh     string
		forbidden   bool
		expectFound bool
	}{
		{
			name:        "extracts refresh token from json",
			body:        `{"refresh_token":"refresh-123"}`,
			refresh:     "refresh-123",
			expectFound: true,
		},
		{
			name:      "detects forbidden location",
			body:      `{"error_description":"forbidden-location"}`,
			forbidden: true,
		},
		{
			name:      "detects html forbidden response",
			body:      `403 ERROR`,
			forbidden: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			refresh, forbidden, found := parseDisneyTokenResponse([]byte(tt.body))
			if refresh != tt.refresh {
				t.Fatalf("refresh = %q, want %q", refresh, tt.refresh)
			}
			if forbidden != tt.forbidden {
				t.Fatalf("forbidden = %v, want %v", forbidden, tt.forbidden)
			}
			if found != tt.expectFound {
				t.Fatalf("found = %v, want %v", found, tt.expectFound)
			}
		})
	}
}

func TestParseDisneyGraphQLRegion(t *testing.T) {
	t.Parallel()

	region, supported, ok := parseDisneyGraphQLRegion([]byte(`{"extensions":{"sdk":{"session":{"location":{"countryCode":"JP"},"inSupportedLocation":true}}}}`))
	if !ok {
		t.Fatal("parseDisneyGraphQLRegion returned false")
	}
	if region != "JP" {
		t.Fatalf("region = %q, want JP", region)
	}
	if !supported {
		t.Fatal("supported = false, want true")
	}
}

func TestParseDisneyMainPageRegion(t *testing.T) {
	t.Parallel()

	region, ok := parseDisneyMainPageRegion([]byte(`{"region":"US"}`))
	if !ok {
		t.Fatal("parseDisneyMainPageRegion returned false")
	}
	if region != "US" {
		t.Fatalf("region = %q, want US", region)
	}
}

func TestIsDisneyPreviewUnavailableURL(t *testing.T) {
	t.Parallel()

	if !isDisneyPreviewUnavailableURL("https://www.disneyplus.com/unavailable") {
		t.Fatal("unavailable URL was not detected")
	}
	if !isDisneyPreviewUnavailableURL("https://www.disneyplus.com/preview") {
		t.Fatal("preview URL was not detected")
	}
	if isDisneyPreviewUnavailableURL("https://www.disneyplus.com/home") {
		t.Fatal("home URL was detected as unavailable")
	}
}
