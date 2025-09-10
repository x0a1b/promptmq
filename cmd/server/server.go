package server

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/x0a1b/promptmq/internal/broker"
	"github.com/x0a1b/promptmq/internal/config"
)

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the PromptMQ MQTT broker server",
	Long: `Start the PromptMQ MQTT broker server with all configured options.
The server supports MQTT v5 with backward compatibility for v3.1.1 and v3.0.`,
	RunE: runServer,
}

func init() {
	// Server configuration
	ServerCmd.Flags().String("bind", "0.0.0.0:1883", "TCP bind address for MQTT")
	ServerCmd.Flags().String("ws-bind", "0.0.0.0:8080", "WebSocket bind address")
	ServerCmd.Flags().Bool("tls", false, "Enable TLS for MQTT connections")
	ServerCmd.Flags().String("tls-cert", "", "TLS certificate file path")
	ServerCmd.Flags().String("tls-key", "", "TLS private key file path")

	// Storage configuration
	ServerCmd.Flags().String("data-dir", "./data", "Data directory for persistent storage")
	ServerCmd.Flags().String("wal-dir", "./wal", "WAL directory for write-ahead logging")
	ServerCmd.Flags().Uint64("memory-buffer", 268435456, "Memory buffer size in bytes (default 256MB)")
	ServerCmd.Flags().Duration("wal-sync-interval", time.Millisecond*100, "WAL sync interval")
	ServerCmd.Flags().Bool("wal-no-sync", false, "Disable WAL fsync for better performance (less durability)")

	// MQTT protocol configuration
	ServerCmd.Flags().Uint16("max-packet-size", 65535, "Maximum MQTT packet size")
	ServerCmd.Flags().Uint16("max-client-id-len", 65535, "Maximum client ID length")
	ServerCmd.Flags().Uint16("max-topic-len", 65535, "Maximum topic length")
	ServerCmd.Flags().Uint8("max-qos", 2, "Maximum QoS level (0, 1, 2)")
	ServerCmd.Flags().Uint16("keep-alive", 60, "Default keep-alive interval in seconds")
	ServerCmd.Flags().Uint32("max-inflight", 20, "Maximum in-flight messages per client")
	ServerCmd.Flags().Bool("retain-available", true, "Enable retained message support")
	ServerCmd.Flags().Bool("wildcard-available", true, "Enable wildcard subscription support")
	ServerCmd.Flags().Bool("shared-sub-available", true, "Enable shared subscription support")

	// Performance tuning
	ServerCmd.Flags().Uint32("max-connections", 10000, "Maximum concurrent connections")
	ServerCmd.Flags().Uint32("read-buffer", 4096, "Socket read buffer size")
	ServerCmd.Flags().Uint32("write-buffer", 4096, "Socket write buffer size")
	ServerCmd.Flags().Duration("read-timeout", time.Second*30, "Socket read timeout")
	ServerCmd.Flags().Duration("write-timeout", time.Second*30, "Socket write timeout")

	// Clustering configuration
	ServerCmd.Flags().Bool("cluster", false, "Enable clustering mode")
	ServerCmd.Flags().String("cluster-bind", "0.0.0.0:7946", "Cluster bind address")
	ServerCmd.Flags().StringSlice("cluster-peers", nil, "Cluster peer addresses")
	ServerCmd.Flags().String("node-id", "", "Node ID for clustering (auto-generated if empty)")

	// Monitoring configuration
	ServerCmd.Flags().Bool("metrics", true, "Enable metrics endpoint")
	ServerCmd.Flags().String("metrics-bind", "0.0.0.0:9090", "Metrics endpoint bind address")
	ServerCmd.Flags().String("metrics-path", "/metrics", "Metrics endpoint path")

	// Daemon configuration
	ServerCmd.Flags().Bool("daemon", false, "Run as daemon (background process)")
	ServerCmd.Flags().String("pid-file", "", "PID file path for daemon mode")

	// Bind all flags to viper
	viper.BindPFlags(ServerCmd.Flags())
}

func runServer(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Handle daemon mode
	if cfg.Server.Daemon {
		return runDaemon(cfg)
	}

	// Create and start broker
	b, err := broker.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create broker: %w", err)
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		fmt.Printf("\nReceived signal: %v\n", sig)
		cancel()
	}()

	// Start broker
	if err := b.Start(ctx); err != nil {
		return fmt.Errorf("failed to start broker: %w", err)
	}

	fmt.Println("PromptMQ broker stopped")
	return nil
}

func runDaemon(cfg *config.Config) error {
	// TODO: Implement proper daemonization
	// For now, just run in foreground
	fmt.Println("Daemon mode not yet implemented, running in foreground")

	// Create and start broker directly without calling runServer
	b, err := broker.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create broker: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	return b.Start(ctx)
}
