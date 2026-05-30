package main

import (
	"context"
	"log"
	"net/http"

	"dropoutbox/internal/buildinfo"
	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/router"
	"dropoutbox/internal/service"
	"dropoutbox/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()

	var storageRuntime *storage.Runtime
	if cfg.App.Storage {
		storageRuntime, err = storage.NewRuntime(cfg)
		if err != nil {
			log.Fatalf("init storage runtime: %v", err)
		}
		storageRuntime.Start(ctx)
	}

	var authService *service.AuthService
	var userService *service.UserService
	var roleService *service.RoleService
	var nodeService *service.NodeService
	var inventoryService *service.InventoryService
	var replicaService *service.ReplicaService

	if cfg.App.Coordinator {
		database, err := db.Open(cfg.Database)
		if err != nil {
			log.Fatalf("open database: %v", err)
		}

		if cfg.Database.AutoMigrate {
			if err := db.AutoMigrate(database); err != nil {
				log.Fatalf("migrate database: %v", err)
			}
		}

		userRepo := repository.NewUserRepository(database)
		userTokenRepo := repository.NewUserTokenRepository(database)
		nodeRepo := repository.NewNodeRepository(database)
		nodeCommandRepo := repository.NewNodeCommandRepository(database)
		nodeTokenRepo := repository.NewNodeTokenRepository(database)
		roleRepo := repository.NewRoleRepository(database)
		inventoryRepo := repository.NewInventoryRepository(database)
		replicaRepo := repository.NewReplicaRepository(database)
		settingRepo := repository.NewSettingRepository(database)
		settingService := service.NewSettingService(settingRepo)

		authService = service.NewAuthService(
			userRepo,
			userTokenRepo,
			nodeRepo,
			nodeTokenRepo,
			cfg.Auth.JWTSecret,
			cfg.Auth.AccessTokenDuration,
			cfg.Auth.RefreshTokenDuration,
			settingService,
		)
		userService = service.NewUserService(userRepo, roleRepo)
		roleService = service.NewRoleService(roleRepo)
		nodeService = service.NewNodeService(nodeRepo, nodeCommandRepo)
		inventoryService = service.NewInventoryService(inventoryRepo, nodeService)
		replicaService = service.NewReplicaService(replicaRepo, inventoryRepo, nodeService, settingService)
	}

	handler := router.New(
		cfg,
		buildinfo.Get(),
		authService,
		userService,
		roleService,
		nodeService,
		inventoryService,
		replicaService,
		storageRuntime,
	)

	server := &http.Server{
		Addr:    cfg.HTTP.Address,
		Handler: handler,
	}

	log.Printf(
		"starting %s version=%s node_id=%s coordinator=%t storage=%t listen=%s",
		"DropOutBox",
		buildinfo.Version,
		cfg.App.NodeID,
		cfg.App.Coordinator,
		cfg.App.Storage,
		cfg.HTTP.Address,
	)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("run api: %v", err)
	}
}
