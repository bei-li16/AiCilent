package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/server"
	"ai-proxy/internal/version"
)

func main() {
	configPath := flag.String("config", "config/providers.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "show version info")
	flag.Parse()

	if *showVersion {
		fmt.Fprint(os.Stdout, version.String())
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	srv := server.New(cfg, *configPath)
	log.Printf("%s", version.String())
	log.Printf("AI Proxy starting on %s", cfg.Global.ListenAddr)

	// Start server in background
	httpServer := &http.Server{
		Addr:              cfg.Global.ListenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Fatalf("Shutdown error: %v", err)
	}

	// Stop config watcher goroutine
	srv.StopWatcher()

	// Close log rotator
	if srv.Rot != nil {
		srv.Rot.Close()
	}

	log.Println("Server stopped")
}