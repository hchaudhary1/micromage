package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"micromage/internal/web"
)

//go:embed web/templates/*.html web/static/* web/workflows/*.yaml web/commands/*.md
var assets embed.FS

const (
	defaultHost              = "127.0.0.1"
	defaultPort              = "8080"
	defaultReadTimeout       = 15 * time.Second
	defaultReadHeaderTimeout = 5 * time.Second
	defaultWriteTimeout      = 30 * time.Minute
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
)

var (
	buildVersion = "development"
	buildCommit  = "unknown"
	buildDate    = "unknown"
)

type serverConfig struct {
	Host              string
	Port              string
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func main() {
	showVersion := flag.Bool("version", false, "print build metadata and exit")
	flag.Parse()

	serverMetadata := web.BuildInfo{
		Version:   buildVersion,
		Commit:    buildCommit,
		BuildDate: buildDate,
	}
	if *showVersion {
		payload, err := json.MarshalIndent(serverMetadata, "", "  ")
		if err != nil {
			log.Fatalf("encode build metadata: %v", err)
		}
		fmt.Printf("%s\n", string(payload))
		return
	}

	server, err := web.NewServer(assets, serverMetadata)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	config, err := loadServerConfig(os.Getenv)
	if err != nil {
		log.Fatalf("load server config: %v", err)
	}

	httpServer := newHTTPServer(config, server)
	listener, err := net.Listen("tcp", httpServer.Addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", httpServer.Addr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serveHTTP(ctx, httpServer, listener, config.ShutdownTimeout, log.Default()); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func loadServerConfig(getenv func(string) string) (serverConfig, error) {
	port := getenv("MICROMAGE_PORT")
	if port == "" {
		port = getenv("PORT")
	}
	if port == "" {
		port = defaultPort
	}
	host := getenv("MICROMAGE_HOST")
	if host == "" {
		// Local-only binding avoids exposing workflow controls unless operators opt in.
		host = defaultHost
	}

	readTimeout, err := envDuration(getenv, "MICROMAGE_READ_TIMEOUT", defaultReadTimeout)
	if err != nil {
		return serverConfig{}, err
	}
	readHeaderTimeout, err := envDuration(getenv, "MICROMAGE_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout)
	if err != nil {
		return serverConfig{}, err
	}
	writeTimeout, err := envDuration(getenv, "MICROMAGE_WRITE_TIMEOUT", defaultWriteTimeout)
	if err != nil {
		return serverConfig{}, err
	}
	idleTimeout, err := envDuration(getenv, "MICROMAGE_IDLE_TIMEOUT", defaultIdleTimeout)
	if err != nil {
		return serverConfig{}, err
	}
	shutdownTimeout, err := envDuration(getenv, "MICROMAGE_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return serverConfig{}, err
	}

	return serverConfig{
		Host:              host,
		Port:              port,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ShutdownTimeout:   shutdownTimeout,
	}, nil
}

func envDuration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	value := getenv(key)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	return duration, nil
}

func newHTTPServer(config serverConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort(config.Host, config.Port),
		Handler:           handler,
		ReadTimeout:       config.ReadTimeout,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
	}
}

func serveHTTP(ctx context.Context, httpServer *http.Server, listener net.Listener, shutdownTimeout time.Duration, logger *log.Logger) error {
	if logger != nil {
		logger.Printf("Micromage Workflows listening on %s", httpURL(listener.Addr()))
	}
	errc := make(chan error, 1)
	go func() {
		errc <- httpServer.Serve(listener)
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	// Shutdown gives in-flight workflow requests a short window to finish cleanly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()
		if serveErr := <-errc; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func httpURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	return "http://" + net.JoinHostPort(host, port)
}
