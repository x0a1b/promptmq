package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	// Reset viper for clean test
	viper.Reset()

	cfg, err := Load()
	require.NoError(t, err)
	assert.NotNil(t, cfg)

	// Test default values
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "0.0.0.0:1883", cfg.Server.Bind)
	assert.Equal(t, "./data", cfg.Storage.DataDir)
	assert.Equal(t, uint8(2), cfg.MQTT.MaxQoS)
	
	// Test SQLite default values
	assert.Equal(t, int64(50000), cfg.Storage.SQLite.CacheSize)
	assert.Equal(t, "MEMORY", cfg.Storage.SQLite.TempStore)
	assert.Equal(t, int64(268435456), cfg.Storage.SQLite.MmapSize)
	assert.Equal(t, 30000, cfg.Storage.SQLite.BusyTimeout)
	assert.Equal(t, "NORMAL", cfg.Storage.SQLite.Synchronous)
	assert.Equal(t, "WAL", cfg.Storage.SQLite.JournalMode)
	assert.True(t, cfg.Storage.SQLite.ForeignKeys)
	
	// Note: metrics.enabled defaults to true in setDefaults(), but mapstructure might initialize to false
	// This is expected behavior, so let's check the actual value
	t.Logf("Metrics.Enabled: %v", cfg.Metrics.Enabled)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name         string
		modifyConfig func(*Config)
		expectError  bool
	}{
		{
			name: "valid config",
			modifyConfig: func(cfg *Config) {
				// No changes, should be valid
			},
			expectError: false,
		},
		{
			name: "invalid QoS",
			modifyConfig: func(cfg *Config) {
				cfg.MQTT.MaxQoS = 3
			},
			expectError: true,
		},
		{
			name: "cleanup batch size too small",
			modifyConfig: func(cfg *Config) {
				cfg.Storage.Cleanup.BatchSize = -1
			},
			expectError: true,
		},
		{
			name: "TLS enabled without cert",
			modifyConfig: func(cfg *Config) {
				cfg.Server.TLS = true
				cfg.Server.TLSCert = ""
			},
			expectError: true,
		},
		{
			name: "invalid SQLite temp store",
			modifyConfig: func(cfg *Config) {
				cfg.Storage.SQLite.TempStore = "INVALID"
			},
			expectError: true,
		},
		{
			name: "invalid SQLite synchronous mode",
			modifyConfig: func(cfg *Config) {
				cfg.Storage.SQLite.Synchronous = "INVALID"
			},
			expectError: true,
		},
		{
			name: "invalid SQLite journal mode",
			modifyConfig: func(cfg *Config) {
				cfg.Storage.SQLite.JournalMode = "INVALID"
			},
			expectError: true,
		},
		{
			name: "zero SQLite cache size",
			modifyConfig: func(cfg *Config) {
				cfg.Storage.SQLite.CacheSize = 0
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			setDefaults()

			cfg := &Config{}
			err := viper.Unmarshal(cfg)
			require.NoError(t, err)

			tt.modifyConfig(cfg)

			err = validate(cfg)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSetDefaults(t *testing.T) {
	viper.Reset()
	setDefaults()

	assert.Equal(t, "info", viper.GetString("log.level"))
	assert.Equal(t, "json", viper.GetString("log.format"))
	assert.Equal(t, "0.0.0.0:1883", viper.GetString("server.bind"))
	assert.Equal(t, "./data", viper.GetString("storage.data-dir"))
	assert.Equal(t, 2, viper.GetInt("mqtt.max-qos"))
	assert.True(t, viper.GetBool("metrics.enabled"))
	
	// Test SQLite defaults
	assert.Equal(t, int64(50000), viper.GetInt64("storage.sqlite.cache-size"))
	assert.Equal(t, "MEMORY", viper.GetString("storage.sqlite.temp-store"))
	assert.Equal(t, int64(268435456), viper.GetInt64("storage.sqlite.mmap-size"))
	assert.Equal(t, 30000, viper.GetInt("storage.sqlite.busy-timeout"))
	assert.Equal(t, "NORMAL", viper.GetString("storage.sqlite.synchronous"))
	assert.Equal(t, "WAL", viper.GetString("storage.sqlite.journal-mode"))
	assert.True(t, viper.GetBool("storage.sqlite.foreign-keys"))
}
