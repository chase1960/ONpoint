package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Apply only explicit per-client settings. Other HTTP clients and system settings
// are unaffected; the standard transport keeps TLS verification enabled.
func (client *JEVClient) withNetwork(proxyValue, timeoutValue string) *JEVClient {
	if client.httpClient == nil {
		return client
	}
	fail := func(reason string) *JEVClient {
		client.reason, client.httpClient = reason, nil
		return client
	}
	if timeoutValue != "" {
		milliseconds, err := strconv.Atoi(timeoutValue)
		if err != nil || milliseconds < 500 || milliseconds > 30000 {
			return fail("invalid_timeout")
		}
		client.httpClient.Timeout = time.Duration(milliseconds) * time.Millisecond
	}
	if proxyValue == "" {
		return client
	}
	proxy, err := url.Parse(proxyValue)
	if err != nil || proxy.Hostname() == "" || (proxy.Scheme != "http" && proxy.Scheme != "https") ||
		proxy.User != nil || proxy.RawQuery != "" || proxy.ForceQuery || proxy.Fragment != "" ||
		proxy.Opaque != "" || proxy.RawPath != "" || (proxy.Path != "" && proxy.Path != "/") {
		return fail("invalid_proxy_url")
	}
	if proxy.Port() != "" {
		port, err := strconv.Atoi(proxy.Port())
		if err != nil || port < 1 || port > 65535 {
			return fail("invalid_proxy_url")
		}
	}
	transport := client.httpClient.Transport.(*http.Transport)
	transport.Proxy = func(request *http.Request) (*url.URL, error) {
		host := request.URL.Hostname()
		if host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
			return nil, nil
		}
		return proxy, nil
	}
	return client
}

type JEVConnectionCheck struct {
	Status       string
	Models       []string
	ProxyEnabled bool
	InferenceRun bool
}

// Check authentication through exactly the same Go transport as Judge, without
// sending state/questions, opening a database, or issuing an inference request.
func (client *JEVClient) CheckModels() (JEVConnectionCheck, error) {
	result := JEVConnectionCheck{Status: "failed"}
	if client == nil || !client.Status().Configured {
		return result, errors.New("JEV connection is not configured")
	}
	endpoint, _ := url.Parse(client.endpoint)
	endpoint.Path = "/v1/models"
	ctx, cancel := context.WithTimeout(context.Background(), client.httpClient.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return result, errors.New("JEV model-list request is invalid")
	}
	transport := client.httpClient.Transport.(*http.Transport)
	if transport.Proxy != nil {
		proxy, _ := transport.Proxy(request)
		result.ProxyEnabled = proxy != nil
	}
	request.Header.Set("Authorization", "Bearer "+client.key)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return result, fmt.Errorf("JEV model-list connection failed: %s", jevNetworkError(err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("JEV model-list HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, jevResponseLimit+1))
	if err != nil || len(data) > jevResponseLimit {
		return result, errors.New("JEV model-list response could not be read")
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &body); err != nil || len(body.Models) == 0 {
		return result, errors.New("JEV model-list response is invalid")
	}
	for _, model := range body.Models {
		if !jevValidModel(model.Name, client.key) || !strings.HasPrefix(model.Name, "jev-") {
			return result, errors.New("JEV model-list name is invalid")
		}
		result.Models = append(result.Models, model.Name)
	}
	result.Status = "ok"
	return result, nil
}
