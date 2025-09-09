package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Log     LogConfig     `mapstructure:"log"`
	Server  ServerConfig  `mapstructure:"server"`
	MQTT    MQTTConfig    `mapstructure:"mqtt"`
	Storage StorageConfig `mapstructure:"storage"`
	Cluster ClusterConfig `mapstructure:"cluster"`
	Metrics MetricsConfig `mapstructure:"metrics"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type ServerConfig struct {
	Bind         string        `mapstructure:"bind"`
	WSBind       string        `mapstructure:"ws-bind"`
	TLS          bool          `mapstructure:"tls"`
	TLSCert      string        `mapstructure:"tls-cert"`
	TLSKey       string        `mapstructure:"tls-key"`
	ReadBuffer   uint32        `mapstructure:"read-buffer"`
	WriteBuffer  uint32        `mapstructure:"write-buffer"`
	ReadTimeout  time.Duration `mapstructure:"read-timeout"`
	WriteTimeout time.Duration `mapstructure:"write-timeout"`
	Daemon       bool          `mapstructure:"daemon"`
	PIDFile      string        `mapstructure:"pid-file"`
}

type MQTTConfig struct {
	MaxPacketSize      uint16 `mapstructure:"max-packet-size"`
	MaxClientIDLen     uint16 `mapstructure:"max-client-id-len"`
	MaxTopicLen        uint16 `mapstructure:"max-topic-len"`
	MaxQoS             uint8  `mapstructure:"max-qos"`
	KeepAlive          uint16 `mapstructure:"keep-alive"`
	MaxInflight        uint32 `mapstructure:"max-inflight"`
	MaxConnections     uint32 `mapstructure:"max-connections"`
	RetainAvailable    bool   `mapstructure:"retain-available"`
	WildcardAvailable  bool   `mapstructure:"wildcard-available"`
	SharedSubAvailable bool   `mapstructure:"shared-sub-available"`
}

type StorageConfig struct {
	DataDir         string           `mapstructure:"data-dir"`
	WALDir          string           `mapstructure:"wal-dir"`
	MemoryBuffer    uint64           `mapstructure:"memory-buffer"`
	WALSyncInterval time.Duration    `mapstructure:"wal-sync-interval"`
	WALNoSync       bool             `mapstructure:"wal-no-sync"`
	WAL             WALConfig        `mapstructure:"wal"`
	Compaction      CompactionConfig `mapstructure:"compaction"`
}

type WALConfig struct {
	SyncMode                string        `mapstructure:"sync-mode"`
	SyncInterval            time.Duration `mapstructure:"sync-interval"`
	BatchSyncSize           int           `mapstructure:"batch-sync-size"`
	ForceFsync              bool          `mapstructure:"force-fsync"`
	CrashRecoveryValidation bool          `mapstructure:"crash-recovery-validation"`
}

type CompactionConfig struct {
	MaxMessageAge     time.Duration `mapstructure:"max-message-age"`
	MaxWALSize        uint64        `mapstructure:"max-wal-size"`
	CheckInterval     time.Duration `mapstructure:"check-interval"`
	ConcurrentWorkers int           `mapstructure:"concurrent-workers"`
	BatchSize         int           `mapstructure:"batch-size"`
}

type ClusterConfig struct {
	Enabled bool     `mapstructure:"cluster"`
	Bind    string   `mapstructure:"cluster-bind"`
	Peers   []string `mapstructure:"cluster-peers"`
	NodeID  string   `mapstructure:"node-id"`
}

type MetricsConfig struct {
	Enabled bool   `mapstructure:"metrics"`
	Bind    string `mapstructure:"metrics-bind"`
	Path    string `mapstructure:"metrics-path"`
}

func Load() (*Config, error) {
	var cfg Config

	// Set defaults
	setDefaults()

	// Unmarshal configuration
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate configuration
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

func setDefaults() {
	// Log defaults
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "json")

	// Server defaults
	viper.SetDefault("server.bind", "0.0.0.0:1883")
	viper.SetDefault("server.ws-bind", "0.0.0.0:8080")
	viper.SetDefault("server.tls", false)
	viper.SetDefault("server.read-buffer", 4096)
	viper.SetDefault("server.write-buffer", 4096)
	viper.SetDefault("server.read-timeout", "30s")
	viper.SetDefault("server.write-timeout", "30s")
	viper.SetDefault("server.daemon", false)

	// MQTT defaults
	viper.SetDefault("mqtt.max-packet-size", 65535)
	viper.SetDefault("mqtt.max-client-id-len", 65535)
	viper.SetDefault("mqtt.max-topic-len", 65535)
	viper.SetDefault("mqtt.max-qos", 2)
	viper.SetDefault("mqtt.keep-alive", 60)
	viper.SetDefault("mqtt.max-inflight", 20)
	viper.SetDefault("mqtt.max-connections", 10000)
	viper.SetDefault("mqtt.retain-available", true)
	viper.SetDefault("mqtt.wildcard-available", true)
	viper.SetDefault("mqtt.shared-sub-available", true)

	// Storage defaults
	viper.SetDefault("storage.data-dir", "./data")
	viper.SetDefault("storage.wal-dir", "./wal")
	viper.SetDefault("storage.memory-buffer", 268435456) // 256MB
	viper.SetDefault("storage.wal-sync-interval", "100ms")
	viper.SetDefault("storage.wal-no-sync", false)

	// WAL durability defaults
	viper.SetDefault("storage.wal.sync-mode", "periodic")
	viper.SetDefault("storage.wal.sync-interval", "100ms")
	viper.SetDefault("storage.wal.batch-sync-size", 100)
	viper.SetDefault("storage.wal.force-fsync", false)
	viper.SetDefault("storage.wal.crash-recovery-validation", false)

	// Compaction defaults
	viper.SetDefault("storage.compaction.max-message-age", "2h")
	viper.SetDefault("storage.compaction.max-wal-size", 104857600) // 100MB
	viper.SetDefault("storage.compaction.check-interval", "5m")
	viper.SetDefault("storage.compaction.concurrent-workers", 2)
	viper.SetDefault("storage.compaction.batch-size", 1000)

	// Cluster defaults
	viper.SetDefault("cluster.enabled", false)
	viper.SetDefault("cluster.bind", "0.0.0.0:7946")
	viper.SetDefault("cluster.peers", []string{})

	// Metrics defaults
	viper.SetDefault("metrics.enabled", true)
	viper.SetDefault("metrics.bind", "0.0.0.0:9090")
	viper.SetDefault("metrics.path", "/metrics")
}

func validate(cfg *Config) error {
	// Validate QoS level
	if cfg.MQTT.MaxQoS > 2 {
		return fmt.Errorf("max-qos must be 0, 1, or 2")
	}

	// Validate memory buffer size (minimum 1MB)
	if cfg.Storage.MemoryBuffer < 1048576 {
		return fmt.Errorf("memory-buffer must be at least 1MB (1048576 bytes)")
	}

	// Validate TLS configuration
	if cfg.Server.TLS && (cfg.Server.TLSCert == "" || cfg.Server.TLSKey == "") {
		return fmt.Errorf("tls-cert and tls-key must be provided when TLS is enabled")
	}

	// Validate WAL sync mode
	validSyncModes := map[string]bool{
		"periodic":  true,
		"immediate": true,
		"batch":     true,
	}
	if !validSyncModes[cfg.Storage.WAL.SyncMode] {
		return fmt.Errorf("invalid wal sync-mode: %s, must be one of: periodic, immediate, batch", cfg.Storage.WAL.SyncMode)
	}

	// Validate batch sync size
	if cfg.Storage.WAL.SyncMode == "batch" && cfg.Storage.WAL.BatchSyncSize <= 0 {
		return fmt.Errorf("batch-sync-size must be positive when sync-mode is batch")
	}

	// Validate sync interval
	if cfg.Storage.WAL.SyncMode == "periodic" && cfg.Storage.WAL.SyncInterval <= 0 {
		return fmt.Errorf("sync-interval must be positive when sync-mode is periodic")
	}

	return nil
}
