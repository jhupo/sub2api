package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchWebshareRotatingProxyURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v2/proxy/list/", r.URL.Path)
		require.Equal(t, "backbone", r.URL.Query().Get("mode"))
		require.Empty(t, r.URL.Query().Get("plan_id"))
		require.Equal(t, "Token secret-api-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		// Residential backbone results expose the IP-authorization port even
		// when username/password clients must connect to p.webshare.io:80.
		_, _ = w.Write([]byte(`{"count":1,"results":[{"username":"proxyuser","password":"proxypass","proxy_address":null,"port":10000}]}`))
	}))
	defer server.Close()

	proxyURL, err := fetchWebshareRotatingProxyURL(context.Background(), UpstreamStateSettings{
		WebshareAPIKey:      "secret-api-key",
		WebshareCountryMode: webshareCountryModeSpecified,
		WebshareCountries:   []string{"US"},
	}, server.URL, server.Client())
	require.NoError(t, err)
	parsed, err := url.Parse(proxyURL)
	require.NoError(t, err)
	require.Equal(t, "p.webshare.io:80", parsed.Host)
	require.Equal(t, "proxyuser-us-rotate", parsed.User.Username())
	password, ok := parsed.User.Password()
	require.True(t, ok)
	require.Equal(t, "proxypass", password)
}

func TestFetchWebshareRotatingProxyURLRejectsFailedLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"bad token"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := fetchWebshareRotatingProxyURL(context.Background(), UpstreamStateSettings{WebshareAPIKey: "bad", WebshareCountryMode: webshareCountryModeRandom}, server.URL, server.Client())
	require.EqualError(t, err, "webshare proxy lookup returned HTTP 401")
	require.NotContains(t, err.Error(), "bad token")
}

func TestFetchWebshareRotatingProxyURLUsesGlobalRandomPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":200000,"results":[{"username":"proxyuser","password":"proxypass","proxy_address":null,"port":10000}]}`))
	}))
	defer server.Close()

	proxyURL, err := fetchWebshareRotatingProxyURL(context.Background(), UpstreamStateSettings{
		WebshareAPIKey:      "secret-api-key",
		WebshareCountryMode: webshareCountryModeRandom,
	}, server.URL, server.Client())
	require.NoError(t, err)
	parsed, err := url.Parse(proxyURL)
	require.NoError(t, err)
	require.Equal(t, "p.webshare.io:80", parsed.Host)
	require.Equal(t, "proxyuser-rotate", parsed.User.Username())
}
