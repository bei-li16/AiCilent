package main

import (
	"flag"
	"log"

	"ai-proxy/internal/config"
	"ai-proxy/internal/server"
)

func main() {
	configPath := flag.String("config", "config/providers.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	srv := server.New(cfg)
	log.Printf("AI Proxy starting on %s", cfg.Global.ListenAddr)
	if err := srv.Run(cfg.Global.ListenAddr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}