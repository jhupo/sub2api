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
	Count   int                     `json:"count"`
	Results []webshareBackboneProxy `json:"results"`
}

// fetchWebshareRotatingProxyURL selects one residential backbone credential
// from Webshare's proxy list. Each list item identifies one exit; appending a
// rotation suffix to these credentials makes Webshare reject CONNECT with 400.
func fetchWebshareRotatingProxyURL(ctx context.Context, cfg UpstreamStateSettings, baseURL string, client *http.Client) (string, error) {
	apiKey := strings.TrimSpace(cfg.WebshareAPIKey)
	if apiKey == "" {
		return "", errors.New("webshare API key is not configured")
	}
	if client == nil {
		return "", errors.New("webshare HTTP client is unavailable")
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Webshare API URL: %w", err)
	}
	base.Path = "/api/v2/proxy/list/"
	query := base.Query()
	query.Set("mode", "backbone")
	query.Set("page_size", "1")
	switch cfg.WebshareCountryMode {
	case webshareCountryModeRandom:
	case webshareCountryModeSpecified:
		if len(cfg.WebshareCountries) == 0 {
			return "", errors.New("webshare specified-country mode has no country codes")
		}
		index, randomErr := rand.Int(rand.Reader, big.NewInt(int64(len(cfg.WebshareCountries))))
		if randomErr != nil {
			return "", fmt.Errorf("select Webshare country: %w", randomErr)
		}
		query.Set("country_code__in", cfg.WebshareCountries[index.Int64()])
	default:
		return "", errors.New("invalid Webshare country mode")
	}

	fetchPage := func(page int64) (webshareProxyList, error) {
		query.Set("page", strconv.FormatInt(page, 10))
		base.RawQuery = query.Encode()
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
		if requestErr != nil {
			return webshareProxyList{}, requestErr
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Token "+apiKey)
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			return webshareProxyList{}, fmt.Errorf("webshare proxy lookup failed: %w", requestErr)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, upstreamStateWebshareBodyLimit))
			return webshareProxyList{}, fmt.Errorf("webshare proxy lookup returned HTTP %d", resp.StatusCode)
		}
		var payload webshareProxyList
		decoder := json.NewDecoder(io.LimitReader(resp.Body, upstreamStateWebshareBodyLimit))
		if decodeErr := decoder.Decode(&payload); decodeErr != nil {
			return webshareProxyList{}, fmt.Errorf("invalid Webshare proxy response: %w", decodeErr)
		}
		return payload, nil
	}

	payload, err := fetchPage(1)
	if err != nil {
		return "", err
	}
	if payload.Count <= 0 || len(payload.Results) == 0 {
		return "", errors.New("webshare returned no backbone proxy credentials")
	}
	page, err := rand.Int(rand.Reader, big.NewInt(int64(payload.Count)))
	if err != nil {
		return "", fmt.Errorf("select Webshare proxy: %w", err)
	}
	selectedPage := page.Int64() + 1
	if selectedPage != 1 {
		payload, err = fetchPage(selectedPage)
		if err != nil {
			return "", err
		}
		if len(payload.Results) == 0 {
			return "", errors.New("webshare returned no proxy for selected page")
		}
	}
	credential := payload.Results[0]
	if strings.TrimSpace(credential.Username) == "" || credential.Password == "" {
		return "", errors.New("webshare returned incomplete backbone credentials")
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
		User: url.UserPassword(credential.Username, credential.Password),
	}
	return proxy.String(), nil
}
