package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadServerConfigDefaultsToLocalBindAndProductionTimeouts(t *testing.T) {
	config, err := loadServerConfig(mapGetenv(nil))
	if err != nil {
		t.Fatalf("loadServerConfig returned error: %v", err)
	}

	if config.Host != "127.0.0.1" || config.Port != "8080" {
		t.Fatalf("expected local default bind, got %#v", config)
	}
	if config.ReadTimeout != 15*time.Second || config.ReadHeaderTimeout != 5*time.Second || config.WriteTimeout != 30*time.Minute || config.IdleTimeout != 60*time.Second || config.ShutdownTimeout != 10*time.Second {
		t.Fatalf("expected production timeout defaults, got %#v", config)
	}
}

func TestLoadServerConfigUsesEnvOverrides(t *testing.T) {
	config, err := loadServerConfig(mapGetenv(map[string]string{
		"MICROMAGE_HOST":                "0.0.0.0",
		"MICROMAGE_PORT":                "9090",
		"PORT":                          "3000",
		"MICROMAGE_READ_TIMEOUT":        "7s",
		"MICROMAGE_READ_HEADER_TIMEOUT": "3s",
		"MICROMAGE_WRITE_TIMEOUT":       "2m",
		"MICROMAGE_IDLE_TIMEOUT":        "45s",
		"MICROMAGE_SHUTDOWN_TIMEOUT":    "12s",
	}))
	if err != nil {
		t.Fatalf("loadServerConfig returned error: %v", err)
	}

	if config.Host != "0.0.0.0" || config.Port != "9090" {
		t.Fatalf("expected explicit bind, got %#v", config)
	}
	if config.ReadTimeout != 7*time.Second || config.ReadHeaderTimeout != 3*time.Second || config.WriteTimeout != 2*time.Minute || config.IdleTimeout != 45*time.Second || config.ShutdownTimeout != 12*time.Second {
		t.Fatalf("expected overridden timeouts, got %#v", config)
	}
}

func TestLoadServerConfigFallsBackToPortEnv(t *testing.T) {
	config, err := loadServerConfig(mapGetenv(map[string]string{"PORT": "3000"}))
	if err != nil {
		t.Fatalf("loadServerConfig returned error: %v", err)
	}

	if config.Port != "3000" {
		t.Fatalf("expected PORT fallback, got %#v", config)
	}
}

func TestLoadServerConfigRejectsInvalidDurations(t *testing.T) {
	for _, key := range []string{
		"MICROMAGE_READ_TIMEOUT",
		"MICROMAGE_READ_HEADER_TIMEOUT",
		"MICROMAGE_WRITE_TIMEOUT",
		"MICROMAGE_IDLE_TIMEOUT",
		"MICROMAGE_SHUTDOWN_TIMEOUT",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := loadServerConfig(mapGetenv(map[string]string{key: "soon"}))
			if err == nil {
				t.Fatal("expected invalid duration error")
			}
		})
	}
}

func TestNewHTTPServerAppliesBindAndTimeouts(t *testing.T) {
	config := serverConfig{
		Host:              "127.0.0.1",
		Port:              "8081",
		ReadTimeout:       time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}

	server := newHTTPServer(config, http.NotFoundHandler())

	if server.Addr != "127.0.0.1:8081" || server.ReadTimeout != time.Second || server.ReadHeaderTimeout != 2*time.Second || server.WriteTimeout != 3*time.Second || server.IdleTimeout != 4*time.Second {
		t.Fatalf("expected configured http.Server, got %#v", server)
	}
}

func TestServeHTTPLogsActualAddressAndShutsDownGracefully(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var logs bytes.Buffer
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- serveHTTP(ctx, server, listener, time.Second, log.New(&logs, "", 0))
	}()

	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveHTTP returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
	if !strings.Contains(logs.String(), "Micromage Workflows listening on http://127.0.0.1:") {
		t.Fatalf("expected actual startup address in logs, got %q", logs.String())
	}
}

func TestServeHTTPReturnsListenerErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = listener.Close()

	err = serveHTTP(context.Background(), &http.Server{Handler: http.NotFoundHandler()}, listener, time.Second, nil)

	if err == nil {
		t.Fatal("expected listener error")
	}
}

func TestHTTPURLFallsBackWhenAddressCannotSplit(t *testing.T) {
	got := httpURL(stringAddr("micromage.sock"))

	if got != "http://micromage.sock" {
		t.Fatalf("expected fallback URL, got %q", got)
	}
}

func mapGetenv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

type stringAddr string

func (addr stringAddr) Network() string {
	return "test"
}

func (addr stringAddr) String() string {
	return string(addr)
}

func TestHTTPURLFormatsIPv6Addresses(t *testing.T) {
	got := httpURL(&net.TCPAddr{IP: net.ParseIP("::1"), Port: 8080})

	if got != "http://[::1]:8080" {
		t.Fatalf("expected IPv6 URL, got %q", got)
	}
}

func TestNewHTTPServerFormatsIPv6BindAddress(t *testing.T) {
	server := newHTTPServer(serverConfig{Host: "::1", Port: "8080"}, http.NotFoundHandler())

	if server.Addr != net.JoinHostPort("::1", strconv.Itoa(8080)) {
		t.Fatalf("expected IPv6 bind address, got %q", server.Addr)
	}
}
