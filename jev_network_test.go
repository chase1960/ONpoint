package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJEVExplicitProxyAndTimeout(test *testing.T) {
	jevTestEnv(test)
	test.Setenv("TYPESAFE_API_KEY", "testkey")
	test.Setenv("JEV_MODE", "shadow")
	test.Setenv("TYPESAFE_ALLOW_SYNTHETIC_EGRESS", "true")
	test.Setenv("TYPESAFE_PROXY_URL", "http://127.0.0.1:7890")
	test.Setenv("TYPESAFE_TIMEOUT_MS", "15000")
	client := newJEVFromEnv()
	if !client.Status().Ready || client.httpClient.Timeout != 15*time.Second {
		test.Fatal("explicit network settings not loaded")
	}
	transport := client.httpClient.Transport.(*http.Transport)
	request, _ := http.NewRequest(http.MethodGet, client.endpoint, nil)
	proxy, err := transport.Proxy(request)
	if err != nil || proxy == nil || proxy.String() != "http://127.0.0.1:7890" {
		test.Fatal("explicit proxy not selected")
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		request, _ := http.NewRequest(http.MethodGet, "http://"+host+":1234/v1/models", nil)
		proxy, err := transport.Proxy(request)
		if err != nil || proxy != nil {
			test.Fatal("loopback requests must remain local")
		}
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		test.Fatal("proxy must not disable TLS verification")
	}
	jevAssertNoSecrets(test, client.Status())
	for _, proxy := range []string{"socks5://127.0.0.1:7890", "http://testkey@localhost:7890", "http://localhost:7890/path", "http://localhost:7890?", "http://localhost:0", "http://localhost:65536", "://bad"} {
		test.Setenv("TYPESAFE_PROXY_URL", proxy)
		invalid := newJEVFromEnv()
		if invalid.Status().Configured || invalid.reason != "invalid_proxy_url" {
			test.Fatal("invalid proxy accepted")
		}
		jevAssertNoSecrets(test, invalid.Status())
	}
	test.Setenv("TYPESAFE_PROXY_URL", "")
	for _, value := range []string{"bad", "499", "30001", "1.5"} {
		test.Setenv("TYPESAFE_TIMEOUT_MS", value)
		invalid := newJEVFromEnv()
		if invalid.Status().Configured || invalid.reason != "invalid_timeout" {
			test.Fatal("invalid timeout accepted")
		}
	}
}

func TestJEVHTTPSProxyTunnelForModelsAndJudgment(test *testing.T) {
	var gets, posts, connects atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer testkey" {
			test.Error("upstream auth missing")
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/models":
			gets.Add(1)
			if request.ContentLength != 0 {
				test.Error("model-list check sent data")
			}
			fmt.Fprint(writer, `{"models":[{"name":"jev-latest"},{"name":"jev-preview"}]}`)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/systemone":
			posts.Add(1)
			fmt.Fprint(writer, jevMockResponse)
		default:
			test.Error("unexpected upstream request")
			writer.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect || request.Host != "example.com:443" || request.Header.Get("Authorization") != "" {
			test.Error("CONNECT must address the upstream without exposing its bearer token")
			writer.WriteHeader(400)
			return
		}
		connects.Add(1)
		connection, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			test.Error(err)
			writer.WriteHeader(502)
			return
		}
		defer connection.Close()
		incoming, buffer, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			test.Error(err)
			return
		}
		defer incoming.Close()
		fmt.Fprint(buffer, "HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		go func() { io.Copy(connection, buffer); connection.Close() }()
		io.Copy(incoming, connection)
	}))
	defer proxy.Close()
	client := newJEVClient("testkey", "https://example.com", "", "shadow", "true").withNetwork(proxy.URL, "5000")
	transport := client.httpClient.Transport.(*http.Transport)
	transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	// Trust only this test server, while still checking its example.com certificate.
	check, err := client.CheckModels()
	if err != nil || check.Status != "ok" || !check.ProxyEnabled || check.InferenceRun || len(check.Models) != 2 || posts.Load() != 0 {
		test.Fatalf("model-list check failed: %#v, %v", check, err)
	}
	observation := client.Judge("Synthetic refund progress example")
	if observation.Status != "ok" || gets.Load() != 1 || posts.Load() != 1 || connects.Load() != 2 {
		test.Fatalf("proxy inference contract failed: %s, %s, connects=%d posts=%d gets=%d",
			observation.Status, observation.Error, connects.Load(), posts.Load(), gets.Load())
	}
	jevAssertNoSecrets(test, check)
	jevAssertNoSecrets(test, observation)
}

func TestJEVModelsFailureDoesNotLeakResponse(test *testing.T) {
	for _, scenario := range []struct {
		body string
		code int
	}{
		{`PRIVATE_RESPONSE testkey`, 451},
		{`{"models":[{"name":"testkey"}]}`, 200},
		{`{"models":[]}`, 200},
		{strings.Repeat("x", jevResponseLimit+1), 200},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(scenario.code)
			fmt.Fprint(writer, scenario.body)
		}))
		client := jevTestClient(test, server.URL)
		result, err := client.CheckModels()
		server.Close()
		if err == nil || result.Status != "failed" || result.InferenceRun {
			test.Fatal("failed model-list check reported success")
		}
		jevAssertNoSecrets(test, err.Error())
	}
}
