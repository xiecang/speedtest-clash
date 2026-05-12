package check

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckGPTWebRes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		ok   bool
		msg  string
	}{
		{
			name: "allows when unsupported country is absent",
			body: `{"is_country_supported":true}`,
			ok:   true,
		},
		{
			name: "rejects unsupported country response",
			body: `{"unsupported_country":true}`,
			ok:   false,
			msg:  "Unsupported Country/Region",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ok, msg := checkGPTWebRes(tt.body)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if msg != tt.msg {
				t.Fatalf("msg = %q, want %q", msg, tt.msg)
			}
		})
	}
}

func TestRequestChatGPTUsesGET(t *testing.T) {
	t.Parallel()

	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_, _ = w.Write([]byte(`{"cf_details":"Request is not allowed. Please try again later.", "type":"dc"}`))
	}))
	defer server.Close()

	ok, err := requestChatGPT(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("requestChatGPT returned error: %v", err)
	}
	if !ok {
		t.Fatal("requestChatGPT returned false, want true")
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %s, want %s", gotMethod, http.MethodGet)
	}
}
