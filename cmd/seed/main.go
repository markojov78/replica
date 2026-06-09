package main

import (
	"log"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/seed"
)

func main() {
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
