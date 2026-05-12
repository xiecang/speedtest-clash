package check

import "testing"

func TestParseGeminiRegion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		code string
		ok   bool
	}{
		{
			name: "extracts three-letter region",
			body: `prefix,2,1,200,"USA",suffix`,
			code: "USA",
			ok:   true,
		},
		{
			name: "rejects blocked region",
			body: `prefix,2,1,200,"HKG",suffix`,
			code: "HKG",
			ok:   false,
		},
		{
			name: "fails when marker is absent",
			body: `45631641,null,true`,
			ok:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, found := parseGeminiRegion(tt.body)
			if code != tt.code {
				t.Fatalf("code = %q, want %q", code, tt.code)
			}
			if found != (tt.code != "") {
				t.Fatalf("found = %v, want %v", found, tt.code != "")
			}
			if isGeminiRegionAllowed(code) != tt.ok {
				t.Fatalf("allowed = %v, want %v", isGeminiRegionAllowed(code), tt.ok)
			}
		})
	}
}
