package broker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/x0a1b/promptmq/internal/config"
)

func createTestBrokerConfig(t *testing.T) *config.Config {
	tmpDir := t.TempDir()

	return &config.Config{
		Server: config.ServerConfig{
			Bind:         "localhost:0", // Use port 0 to get a random available port
			WSBind:       "",            // Disable WebSocket for tests
			TLS:          false,
			ReadBuffer:   4096,
			WriteBuffer:  4096,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		MQTT: config.MQTTConfig{
			MaxQoS:               2,
			MaxConnections:       100,
			MaxInflight:          20,
			KeepAlive:            60,
			RetainAvailable:      true,
			WildcardAvailable:    true,
			SharedSubAvailable:   true,
			MaxPacketSize:        65535,
		},
		Storage: config.StorageConfig{
			DataDir: filepath.Join(tmpDir, "data"),
			Cleanup: config.CleanupConfig{
				MaxMessageAge: 24 * time.Hour,
				CheckInterval: 1 * time.Hour,
				BatchSize:     100,
			},
		},
		Metrics: config.MetricsConfig{
			Enabled: false, // Disable metrics for simpler testing
			Bind:    "localhost:0",
			Path:    "/metrics",
		},
		Log: config.LogConfig{
			Level:  "error", // Reduce log noise in tests
			Format: "json",
		},
		Cluster: config.ClusterConfig{
			Enabled: false,
		},
	}
}

// Helper function to start broker in background and wait for it to initialize
func startBrokerAsync(t *testing.T, broker *Broker, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- broker.Start(ctx)
	}()
	
	// Give broker a moment to initialize
	time.Sleep(100 * time.Millisecond)
	return done
}

// Helper function to stop broker and wait for start routine to complete
func stopBrokerAndWait(t *testing.T, broker *Broker, startDone <-chan error, cancel context.CancelFunc) {
	// Cancel context first - this will cause Start() to return and call Stop() internally
	cancel()
	
	// Wait for Start goroutine to complete
	select {
	case startErr := <-startDone:
		assert.NoError(t, startErr)
	case <-time.After(2 * time.Second):
		t.Error("Start method did not complete in time")
	}
}

func TestNewBroker(t *testing.T) {
	cfg := createTestBrokerConfig(t)

	broker, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, broker)

	assert.NotNil(t, broker.cfg)
	assert.NotNil(t, broker.server)
	assert.NotNil(t, broker.storage)
	
	// Cluster should only be non-nil if clustering is enabled
	if cfg.Cluster.Enabled {
		assert.NotNil(t, broker.cluster)
	} else {
		assert.Nil(t, broker.cluster)
	}
	
	assert.Equal(t, cfg, broker.cfg)

	// Clean up
	err = broker.Stop()
	assert.NoError(t, err)
}

func TestNewBrokerWithInvalidConfig(t *testing.T) {
	// Test with invalid storage directory
	cfg := createTestBrokerConfig(t)
	cfg.Storage.DataDir = "/nonexistent/invalid/path"

	broker, err := New(cfg)
	assert.Error(t, err)
	assert.Nil(t, broker)
}

func TestBrokerStart(t *testing.T) {
	cfg := createTestBrokerConfig(t)

	broker, err := New(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := startBrokerAsync(t, broker, ctx)
	stopBrokerAndWait(t, broker, startDone, cancel)
}

func TestBrokerStartStop(t *testing.T) {
	// Test multiple start/stop cycles with fresh broker instances
	// This is necessary because MQTT server can't be restarted after being closed
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("cycle_%d", i), func(t *testing.T) {
			cfg := createTestBrokerConfig(t)

			broker, err := New(cfg)
			require.NoError(t, err, "Failed to create broker on iteration %d", i)

			ctx, cancel := context.WithCancel(context.Background())
			
			startDone := startBrokerAsync(t, broker, ctx)

			time.Sleep(50 * time.Millisecond)

			stopBrokerAndWait(t, broker, startDone, cancel)

			time.Sleep(50 * time.Millisecond)
		})
	}
}

func TestBrokerWithMetrics(t *testing.T) {
	cfg := createTestBrokerConfig(t)
	cfg.Metrics.Enabled = true

	broker, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, broker.metrics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := startBrokerAsync(t, broker, ctx)
	stopBrokerAndWait(t, broker, startDone, cancel)
}

func TestBrokerWithCluster(t *testing.T) {
	cfg := createTestBrokerConfig(t)
	cfg.Cluster.Enabled = true

	broker, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, broker.cluster)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := startBrokerAsync(t, broker, ctx)
	stopBrokerAndWait(t, broker, startDone, cancel)
}

func TestBrokerStopWithoutStart(t *testing.T) {
	cfg := createTestBrokerConfig(t)

	broker, err := New(cfg)
	require.NoError(t, err)

	// Stop without starting should not error
	err = broker.Stop()
	assert.NoError(t, err)
}

func TestSetupLogger(t *testing.T) {
	tests := []struct {
		name     string
		logLevel string
		format   string
	}{
		{"Debug JSON", "debug", "json"},
		{"Info Console", "info", "console"},
		{"Error JSON", "error", "json"},
		{"Warn Console", "warn", "console"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Log: config.LogConfig{
					Level:  tt.logLevel,
					Format: tt.format,
				},
			}

			logger := setupLogger(cfg)
			assert.NotEqual(t, zerolog.Nop(), logger)

			// Test that logger can be used
			logger.Info().Msg("test log message")
		})
	}
}

func TestSetupLoggerInvalidLevel(t *testing.T) {
	cfg := &config.Config{
		Log: config.LogConfig{
			Level:  "invalid-level",
			Format: "json",
		},
	}

	logger := setupLogger(cfg)
	// Should not panic and should return a valid logger
	assert.NotEqual(t, zerolog.Nop(), logger)

	// Test that logger can be used
	logger.Info().Msg("test log message with invalid level")
}

func TestBrokerConcurrentStartStop(t *testing.T) {
	cfg := createTestBrokerConfig(t)

	broker, err := New(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test concurrent start and context cancellation operations
	done := make(chan error, 1)

	// Start broker in background
	go func() {
		done <- broker.Start(ctx)
	}()

	// Let broker initialize
	time.Sleep(50 * time.Millisecond)

	// Cancel context to trigger shutdown
	cancel()

	// Wait for Start to complete
	select {
	case startErr := <-done:
		assert.NoError(t, startErr, "Start should complete cleanly when context is cancelled")
	case <-time.After(2 * time.Second):
		t.Error("Start method did not complete after context cancellation")
	}
}

func TestBrokerContextCancellation(t *testing.T) {
	cfg := createTestBrokerConfig(t)

	broker, err := New(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Start broker in background - it should return when context is cancelled
	done := make(chan error, 1)
	go func() {
		done <- broker.Start(ctx)
	}()

	// Wait for context to be cancelled
	<-ctx.Done()

	// Wait for Start to return due to context cancellation
	select {
	case startErr := <-done:
		assert.NoError(t, startErr, "Start should return cleanly when context is cancelled")
	case <-time.After(1 * time.Second):
		t.Error("Start method did not return after context cancellation")
	}

	// No need to call broker.Stop() - it's already called internally by Start() when context is cancelled
}

func TestBrokerWithTLS(t *testing.T) {
	cfg := createTestBrokerConfig(t)
	cfg.Server.TLS = true
	cfg.Server.TLSCert = "nonexistent.crt" // This will cause listener to fail, but broker creation should succeed
	cfg.Server.TLSKey = "nonexistent.key"

	broker, err := New(cfg)
	require.NoError(t, err) // Broker creation should succeed

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start might fail due to missing cert files, but should not crash
	_ = broker.Start(ctx) // Ignore error as cert files don't exist

	err = broker.Stop()
	assert.NoError(t, err)
}

func TestBrokerWithWebSocket(t *testing.T) {
	cfg := createTestBrokerConfig(t)
	cfg.Server.WSBind = "localhost:0" // Enable WebSocket with random port

	broker, err := New(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := startBrokerAsync(t, broker, ctx)
	stopBrokerAndWait(t, broker, startDone, cancel)
}

func TestBrokerResourceCleanup(t *testing.T) {
	cfg := createTestBrokerConfig(t)

	broker, err := New(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := startBrokerAsync(t, broker, ctx)

	// Verify resources are initialized
	assert.NotNil(t, broker.server)
	assert.NotNil(t, broker.storage)
	
	if cfg.Metrics.Enabled {
		assert.NotNil(t, broker.metrics)
	}

	stopBrokerAndWait(t, broker, startDone, cancel)

	// After stop, resources should still be accessible for potential restart
	assert.NotNil(t, broker.server)
	assert.NotNil(t, broker.storage)
}

func TestBrokerLoggerSetup(t *testing.T) {
	// Test that logger is properly configured based on config
	originalStdout := os.Stdout
	
	// Temporarily redirect stdout to capture log output
	r, w, _ := os.Pipe()
	os.Stdout = w

	cfg := createTestBrokerConfig(t)
	cfg.Log.Level = "debug"
	cfg.Log.Format = "console"

	broker, err := New(cfg)
	require.NoError(t, err)

	// Test that logger works
	broker.logger.Debug().Msg("test debug message")

	// Restore stdout
	w.Close()
	os.Stdout = originalStdout
	r.Close()

	err = broker.Stop()
	assert.NoError(t, err)
}