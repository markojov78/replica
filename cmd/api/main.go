package main

import (
	"log"

	"dropoutbox/internal/buildinfo"
	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/handler"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/router"
	"dropoutbox/internal/service"
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

	if cfg.App.Coordinator && cfg.Database.AutoMigrate {
		if err := db.AutoMigrate(database); err != nil {
			log.Fatalf("migrate database: %v", err)
		}
	}

	inventoryRepo := repository.NewInventoryRepository(database)
	replicaRepo := repository.NewReplicaRepository(database)
	shareRepo := repository.NewShareRepository(database)

	nodeService := service.NewNodeService(cfg)
	inventoryService := service.NewInventoryService(inventoryRepo)
	replicaService := service.NewReplicaService(replicaRepo)
	shareService := service.NewShareService(shareRepo)

	healthHandler := handler.NewHealthHandler(nodeService)
	inventoryHandler := handler.NewInventoryHandler(inventoryService)
	replicaHandler := handler.NewReplicaHandler(replicaService)
	shareHandler := handler.NewShareHandler(shareService)

	engine := router.New(
		cfg,
		buildinfo.Get(),
		healthHandler,
		inventoryHandler,
		replicaHandler,
		shareHandler,
	)

	log.Printf(
		"starting %s version=%s node_id=%s coordinator=%t storage=%t listen=%s",
		cfg.App.Name,
		buildinfo.Version,
		cfg.App.NodeID,
		cfg.App.Coordinator,
		cfg.App.Storage,
		cfg.HTTP.Address,
	)

	if err := engine.Run(cfg.HTTP.Address); err != nil {
		log.Fatalf("run api: %v", err)
	}
}
