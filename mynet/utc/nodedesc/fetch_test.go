package nodedesc

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchHTTPVerifiesGatewayPayload(t *testing.T) {
	frame := goldenFrame(t)
	publicKey := goldenPublicKey(t)
	expect := goldenExpect()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frame)
	}))
	defer server.Close()

	descriptor, err := FetchHTTP(context.Background(), server.Client(), server.URL,
		expect, publicKey)
	if err != nil {
		t.Fatalf("FetchHTTP: %v", err)
	}
	if descriptor != goldenDescriptor() {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}

func TestFetchHTTPRejectsDeliveryFailures(t *testing.T) {
	publicKey := goldenPublicKey(t)
	expect := goldenExpect()
	frame := goldenFrame(t)

	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"status", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}},
		{"short", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(frame[:451])
		}},
		{"long", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(append(append([]byte{}, frame...), 0))
		}},
		{"tampered", func(w http.ResponseWriter, r *http.Request) {
			bad := append([]byte{}, frame...)
			bad[20] ^= 1
			_, _ = w.Write(bad)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			if _, err := FetchHTTP(context.Background(), server.Client(),
				server.URL, expect, publicKey); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestFetchHTTPRejectsWrongTrustAndContext(t *testing.T) {
	frame := goldenFrame(t)
	publicKey := goldenPublicKey(t)
	expect := goldenExpect()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(frame)
	}))
	defer server.Close()

	wrong := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	wrong[0] = 1
	if _, err := FetchHTTP(context.Background(), server.Client(), server.URL,
		expect, wrong); err == nil {
		t.Fatal("wrong key accepted")
	}
	badPin := expect
	badPin.Pin[0] ^= 1
	if _, err := FetchHTTP(context.Background(), server.Client(), server.URL,
		badPin, publicKey); err == nil {
		t.Fatal("wrong pin accepted")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if _, err := FetchHTTP(ctx, server.Client(), server.URL, expect, publicKey); err == nil {
		t.Fatal("expired context accepted")
	}

	if _, err := FetchHTTP(context.Background(), nil, server.URL, expect, publicKey); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := FetchHTTP(context.Background(), server.Client(), "", expect, publicKey); err == nil {
		t.Fatal("empty URL accepted")
	}
}

func TestFetchHTTPRejectsOversizedBody(t *testing.T) {
	frame := goldenFrame(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("A", 5000)))
	}))
	defer server.Close()
	if _, err := FetchHTTP(context.Background(), server.Client(), server.URL,
		goldenExpect(), goldenPublicKey(t)); err == nil {
		t.Fatal("oversized body accepted")
	}
	_ = frame
}
