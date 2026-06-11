package main

import (
	"embed"
	"log"
	"net/http"
	"os"

	"micromage/internal/web"
)

//go:embed web/templates/*.html web/static/* web/workflows/*.yaml
var assets embed.FS

func main() {
	server, err := web.NewServer(assets)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// A configurable port lets the workflow shell run beside other local tools.
	log.Printf("Micromage Workflows listening on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, server))
}
