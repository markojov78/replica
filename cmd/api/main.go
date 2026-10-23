package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/repository"
	"replica/internal/router"
	"replica/internal/service"
	"replica/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	if buildinfo.PrintVersion(os.Args[1:], os.Stdout) {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var authService *service.AuthService
	var userService *service.UserService
	var roleService *service.RoleService
	var nodeService *service.NodeService
	var inventoryService *service.InventoryService
	var replicaService *service.ReplicaService
	var shareService *service.ShareService
	var configService *service.ConfigService

	if cfg.App.Coordinator {
		database, err := db.Open(cfg.Database)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}

		if cfg.Database.AutoMigrate {
			if err := db.AutoMigrate(database); err != nil {
				return fmt.Errorf("migrate database: %w", err)
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
		shareRepo := repository.NewShareRepository(database)
		settingRepo := repository.NewSettingRepository(database)
		configRepo := repository.NewConfigRepository(database)
		settingService := service.NewSettingService(settingRepo)

		configService = service.NewConfigService(configRepo, cfg)
		if err := configService.Load(log.Printf); err != nil {
			return fmt.Errorf("load database config settings: %w", err)
		}
		cfg = configService.EffectiveConfig()

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
		userService = service.NewUserService(userRepo, roleRepo, userTokenRepo)
		roleService = service.NewRoleService(roleRepo)
		nodeService = service.NewNodeService(nodeRepo, nodeCommandRepo, nodeTokenRepo)
		nodeService.Start(ctx)
		inventoryService = service.NewInventoryService(inventoryRepo, nodeService)
		replicaService = service.NewReplicaService(replicaRepo, inventoryRepo, nodeService, settingService)
		shareService = service.NewShareService(shareRepo, nodeService, func() config.SharingConfig {
			return configService.EffectiveConfig().Sharing
		})
		shareService.Start(ctx)
	}

	var storageRuntime *storage.Runtime
	if cfg.App.Storage {
		storageRuntime, err = storage.NewRuntime(cfg)
		if err != nil {
			return fmt.Errorf("init storage runtime: %w", err)
		}
		storageRuntime.Start(ctx)
		defer func() {
			cancel()
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			runErr = errors.Join(runErr, storageRuntime.Stop(cleanupCtx))
		}()
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
		shareService,
		storageRuntime,
		configService,
	)

	server := &http.Server{
		Addr:    cfg.HTTP.Address,
		Handler: handler,
	}

	log.Printf(
		"starting %s version=%s node_id=%s coordinator=%t storage=%t listen=%s",
		router.ServiceName,
		buildinfo.Version,
		cfg.App.NodeID,
		cfg.App.Coordinator,
		cfg.App.Storage,
		cfg.HTTP.Address,
	)

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("run api: %w", err)
		}
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("shutdown HTTP: %w", err), server.Close())
	}
	return runErr
}
