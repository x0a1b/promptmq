package broker

import (
	"context"
	"fmt"
	"os"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/zohaib-hassan/promptmq/internal/cluster"
	"github.com/zohaib-hassan/promptmq/internal/config"
	"github.com/zohaib-hassan/promptmq/internal/metrics"
	"github.com/zohaib-hassan/promptmq/internal/storage"
)

type Broker struct {
	cfg     *config.Config
	server  *mqtt.Server
	storage *storage.Manager
	metrics *metrics.Server
	cluster *cluster.Manager
	logger  zerolog.Logger
}

func New(cfg *config.Config) (*Broker, error) {
	// Initialize logger
	logger := setupLogger(cfg)

	// Create MQTT server
	server := mqtt.New(&mqtt.Options{
		InlineClient: true,
	})

	// Initialize metrics server first (needed by storage manager)
	metricsServer, err := metrics.New(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics server: %w", err)
	}

	// Initialize storage manager with metrics integration
	storageManager, err := storage.NewWithMetrics(cfg, logger, metricsServer)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage manager: %w", err)
	}

	// Initialize cluster manager if clustering is enabled
	var clusterManager *cluster.Manager
	if cfg.Cluster.Enabled {
		clusterManager, err = cluster.New(cfg, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster manager: %w", err)
		}
	}

	broker := &Broker{
		cfg:     cfg,
		server:  server,
		storage: storageManager,
		metrics: metricsServer,
		cluster: clusterManager,
		logger:  logger,
	}

	// Setup MQTT server hooks
	if err := broker.setupHooks(); err != nil {
		return nil, fmt.Errorf("failed to setup hooks: %w", err)
	}

	// Setup MQTT listeners
	if err := broker.setupListeners(); err != nil {
		return nil, fmt.Errorf("failed to setup listeners: %w", err)
	}

	return broker, nil
}

func (b *Broker) Start(ctx context.Context) error {
	b.logger.Info().Msg("Starting PromptMQ broker")

	// Start storage manager
	if err := b.storage.Start(ctx); err != nil {
		return fmt.Errorf("failed to start storage manager: %w", err)
	}

	// Start metrics server
	if err := b.metrics.Start(ctx); err != nil {
		return fmt.Errorf("failed to start metrics server: %w", err)
	}

	// Start cluster manager if enabled
	if b.cluster != nil {
		if err := b.cluster.Start(ctx); err != nil {
			return fmt.Errorf("failed to start cluster manager: %w", err)
		}
	}

	// Start MQTT server
	go func() {
		if err := b.server.Serve(); err != nil {
			b.logger.Error().Err(err).Msg("MQTT server error")
		}
	}()

	b.logger.Info().
		Str("mqtt_bind", b.cfg.Server.Bind).
		Str("ws_bind", b.cfg.Server.WSBind).
		Bool("tls_enabled", b.cfg.Server.TLS).
		Bool("clustering_enabled", b.cfg.Cluster.Enabled).
		Msg("PromptMQ broker started successfully")

	// Wait for context cancellation
	<-ctx.Done()
	b.logger.Info().Msg("Shutting down PromptMQ broker")

	return b.Stop()
}

func (b *Broker) Stop() error {
	b.logger.Info().Msg("Stopping PromptMQ broker")

	// Stop MQTT server
	if err := b.server.Close(); err != nil {
		b.logger.Error().Err(err).Msg("Failed to close MQTT server")
	}

	// Stop cluster manager
	if b.cluster != nil {
		if err := b.cluster.Stop(); err != nil {
			b.logger.Error().Err(err).Msg("Failed to stop cluster manager")
		}
	}

	// Stop metrics server
	if err := b.metrics.StopServer(); err != nil {
		b.logger.Error().Err(err).Msg("Failed to stop metrics server")
	}

	// Stop storage manager
	if err := b.storage.StopManager(); err != nil {
		b.logger.Error().Err(err).Msg("Failed to stop storage manager")
	}

	b.logger.Info().Msg("PromptMQ broker stopped")
	return nil
}

func (b *Broker) setupHooks() error {
	// Add auth hook (allow all for now, but keep it flexible)
	err := b.server.AddHook(new(auth.AllowHook), nil)
	if err != nil {
		return fmt.Errorf("failed to add auth hook: %w", err)
	}

	// Add storage hook
	err = b.server.AddHook(b.storage, nil)
	if err != nil {
		return fmt.Errorf("failed to add storage hook: %w", err)
	}

	// Add metrics hook
	err = b.server.AddHook(b.metrics, nil)
	if err != nil {
		return fmt.Errorf("failed to add metrics hook: %w", err)
	}

	return nil
}

func (b *Broker) setupListeners() error {
	// Setup TCP listener
	tcp := listeners.NewTCP(listeners.Config{
		ID:      "tcp",
		Address: b.cfg.Server.Bind,
	})

	err := b.server.AddListener(tcp)
	if err != nil {
		return fmt.Errorf("failed to add TCP listener: %w", err)
	}

	// Setup WebSocket listener
	if b.cfg.Server.WSBind != "" {
		ws := listeners.NewWebsocket(listeners.Config{
			ID:      "ws",
			Address: b.cfg.Server.WSBind,
		})
		err = b.server.AddListener(ws)
		if err != nil {
			return fmt.Errorf("failed to add WebSocket listener: %w", err)
		}
	}

	return nil
}

func setupLogger(cfg *config.Config) zerolog.Logger {
	// Set log level
	level, err := zerolog.ParseLevel(cfg.Log.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	// Configure output format
	if cfg.Log.Format == "console" {
		log.Logger = log.Output(zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: time.RFC3339,
		})
	}

	return log.Logger.With().
		Str("component", "broker").
		Logger()
}
