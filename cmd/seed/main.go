package main

import (
	"log"
	"os"
	"replica/internal/buildinfo"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/seed"
)

func main() {
	if buildinfo.PrintVersion(os.Args[1:], os.Stdout) {
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	database, err := db.Open(cfg.Database)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	if err := db.AutoMigrate(database); err != nil {
		log.Fatalf("migrate database: %v", err)
	}

	if err := seed.Run(database, cfg.Seed); err != nil {
		log.Fatalf("seed database: %v", err)
	}

	log.Print("database seed complete")
}
