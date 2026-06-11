package main

import (
	"embed"
	"log"
	"net/http"
	"os"

	"micromage/internal/kanban"
	"micromage/internal/web"
)

//go:embed web/templates/*.html web/static/*
var assets embed.FS

func main() {
	store := kanban.NewStore()
	server, err := web.NewServer(store, assets)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// A configurable port lets the board run beside other local tools.
	log.Printf("Micromage Kanban listening on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, server))
}
