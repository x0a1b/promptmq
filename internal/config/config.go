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
	DataDir    string         `mapstructure:"data-dir"`
	Cleanup    CleanupConfig  `mapstructure:"cleanup"`
	SQLite     SQLiteConfig   `mapstructure:"sqlite"`
}

type SQLiteConfig struct {
	CacheSize    int64  `mapstructure:"cache-size"`     // _cache_size (pages, negative for KB)
	TempStore    string `mapstructure:"temp-store"`    // _temp_store (MEMORY, FILE, DEFAULT)
	MmapSize     int64  `mapstructure:"mmap-size"`     // _mmap_size (bytes)
	BusyTimeout  int    `mapstructure:"busy-timeout"`  // _busy_timeout (milliseconds)
	Synchronous  string `mapstructure:"synchronous"`   // _synchronous (OFF, NORMAL, FULL, EXTRA)
	JournalMode  string `mapstructure:"journal-mode"`  // _journal_mode (DELETE, TRUNCATE, PERSIST, MEMORY, WAL, OFF)
	ForeignKeys  bool   `mapstructure:"foreign-keys"`  // _foreign_keys (ON/OFF)
}

type CleanupConfig struct {
	MaxMessageAge     time.Duration `mapstructure:"max-message-age"`
	CheckInterval     time.Duration `mapstructure:"check-interval"`
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

	// Cleanup defaults (for old message cleanup)
	viper.SetDefault("storage.cleanup.max-message-age", "24h")
	viper.SetDefault("storage.cleanup.check-interval", "1h")
	viper.SetDefault("storage.cleanup.batch-size", 1000)

	// SQLite defaults (optimized for performance)
	viper.SetDefault("storage.sqlite.cache-size", 50000)        // 50k pages (~200MB cache)
	viper.SetDefault("storage.sqlite.temp-store", "MEMORY")     // Store temp tables in memory
	viper.SetDefault("storage.sqlite.mmap-size", 268435456)     // 256MB memory-mapped I/O
	viper.SetDefault("storage.sqlite.busy-timeout", 30000)      // 30 second busy timeout
	viper.SetDefault("storage.sqlite.synchronous", "NORMAL")    // Balance safety vs performance
	viper.SetDefault("storage.sqlite.journal-mode", "WAL")      // Write-Ahead Logging mode
	viper.SetDefault("storage.sqlite.foreign-keys", true)       // Enable foreign key constraints

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

	// Validate TLS configuration
	if cfg.Server.TLS && (cfg.Server.TLSCert == "" || cfg.Server.TLSKey == "") {
		return fmt.Errorf("tls-cert and tls-key must be provided when TLS is enabled")
	}

	// Validate cleanup configuration
	if cfg.Storage.Cleanup.MaxMessageAge <= 0 {
		return fmt.Errorf("max-message-age must be positive")
	}

	if cfg.Storage.Cleanup.CheckInterval <= 0 {
		return fmt.Errorf("check-interval must be positive")
	}

	if cfg.Storage.Cleanup.BatchSize <= 0 {
		return fmt.Errorf("cleanup batch-size must be positive")
	}

	// Validate SQLite configuration
	validTempStores := map[string]bool{"MEMORY": true, "FILE": true, "DEFAULT": true}
	if !validTempStores[cfg.Storage.SQLite.TempStore] {
		return fmt.Errorf("sqlite temp-store must be MEMORY, FILE, or DEFAULT")
	}

	validSynchronous := map[string]bool{"OFF": true, "NORMAL": true, "FULL": true, "EXTRA": true}
	if !validSynchronous[cfg.Storage.SQLite.Synchronous] {
		return fmt.Errorf("sqlite synchronous must be OFF, NORMAL, FULL, or EXTRA")
	}

	validJournalModes := map[string]bool{"DELETE": true, "TRUNCATE": true, "PERSIST": true, "MEMORY": true, "WAL": true, "OFF": true}
	if !validJournalModes[cfg.Storage.SQLite.JournalMode] {
		return fmt.Errorf("sqlite journal-mode must be DELETE, TRUNCATE, PERSIST, MEMORY, WAL, or OFF")
	}

	if cfg.Storage.SQLite.CacheSize == 0 {
		return fmt.Errorf("sqlite cache-size must be non-zero")
	}

	if cfg.Storage.SQLite.MmapSize < 0 {
		return fmt.Errorf("sqlite mmap-size must be non-negative")
	}

	if cfg.Storage.SQLite.BusyTimeout < 0 {
		return fmt.Errorf("sqlite busy-timeout must be non-negative")
	}

	return nil
}
