package main

import (
	"log"
	"net/http"

	"dropoutbox/internal/buildinfo"
	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
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

	userRepo := repository.NewUserRepository(database)
	tokenRepo := repository.NewTokenRepository(database)
	roleRepo := repository.NewRoleRepository(database)

	authService := service.NewAuthService(
		userRepo,
		tokenRepo,
		cfg.Auth.AccessTokenDuration,
		cfg.Auth.RefreshTokenDuration,
	)
	userService := service.NewUserService(userRepo, roleRepo)
	roleService := service.NewRoleService(roleRepo)

	handler := router.New(
		cfg,
		buildinfo.Get(),
		authService,
		userService,
		roleService,
	)

	server := &http.Server{
		Addr:    cfg.HTTP.Address,
		Handler: handler,
	}

	log.Printf(
		"starting %s version=%s node_id=%s coordinator=%t storage=%t listen=%s",
		cfg.App.Name,
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
