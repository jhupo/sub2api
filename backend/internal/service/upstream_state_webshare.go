package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	upstreamStateWebshareAPIBaseURL = "https://proxy.webshare.io"
	upstreamStateWebshareBackbone   = "p.webshare.io"
	upstreamStateWebshareAuthPort   = 80
	upstreamStateWebshareBodyLimit  = 1 << 20
)

type webshareBackboneProxy struct {
	Username     string  `json:"username"`
	Password     string  `json:"password"`
	ProxyAddress *string `json:"proxy_address"`
	Port         int     `json:"port"`
}

type webshareProxyList struct {
	Results []webshareBackboneProxy `json:"results"`
}

// fetchWebshareRotatingProxyURL follows Webshare's residential connection
// model: discover the plan credentials through the authenticated list API,
// then connect through p.webshare.io with the -rotate username suffix. A
// residential result can legitimately have a null proxy_address.
func fetchWebshareRotatingProxyURL(ctx context.Context, cfg UpstreamStateSettings, baseURL string, client *http.Client) (string, error) {
	apiKey := strings.TrimSpace(cfg.WebshareAPIKey)
	if apiKey == "" {
		return "", errors.New("Webshare API key is not configured")
	}
	if client == nil {
		return "", errors.New("Webshare HTTP client is unavailable")
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Webshare API URL: %w", err)
	}
	base.Path = "/api/v2/proxy/list/"
	query := base.Query()
	query.Set("mode", "backbone")
	query.Set("page", "1")
	query.Set("page_size", "1")
	base.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Webshare proxy lookup failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, upstreamStateWebshareBodyLimit))
		return "", fmt.Errorf("Webshare proxy lookup returned HTTP %d", resp.StatusCode)
	}
	var payload webshareProxyList
	decoder := json.NewDecoder(io.LimitReader(resp.Body, upstreamStateWebshareBodyLimit))
	if err = decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("invalid Webshare proxy response: %w", err)
	}
	if len(payload.Results) == 0 {
		return "", errors.New("Webshare returned no backbone proxy credentials")
	}
	credential := payload.Results[0]
	if strings.TrimSpace(credential.Username) == "" || credential.Password == "" {
		return "", errors.New("Webshare returned incomplete backbone credentials")
	}
	username := credential.Username
	switch cfg.WebshareCountryMode {
	case webshareCountryModeRandom:
	case webshareCountryModeSpecified:
		if len(cfg.WebshareCountries) == 0 {
			return "", errors.New("Webshare specified-country mode has no country codes")
		}
		index, randomErr := rand.Int(rand.Reader, big.NewInt(int64(len(cfg.WebshareCountries))))
		if randomErr != nil {
			return "", fmt.Errorf("select Webshare country: %w", randomErr)
		}
		username += "-" + strings.ToLower(cfg.WebshareCountries[index.Int64()])
	default:
		return "", errors.New("invalid Webshare country mode")
	}
	proxy := &url.URL{
		Scheme: "http",
		// The backbone port returned by the proxy-list API is for IP
		// authorization. State refreshes use username/password authentication,
		// whose documented backbone endpoint is p.webshare.io:80.
		Host: net.JoinHostPort(
			upstreamStateWebshareBackbone,
			strconv.Itoa(upstreamStateWebshareAuthPort),
		),
		User: url.UserPassword(username+"-rotate", credential.Password),
	}
	return proxy.String(), nil
}
