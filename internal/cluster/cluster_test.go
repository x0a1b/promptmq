package cluster

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/x0a1b/promptmq/internal/config"
)

func createTestClusterConfig() *config.Config {
	return &config.Config{
		Cluster: config.ClusterConfig{
			Enabled: true,
			NodeID:  "test-node-1",
			Bind:    "localhost:7946",
			Peers: []string{
				"localhost:7947",
				"localhost:7948",
			},
		},
	}
}

func createTestLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).Level(zerolog.Disabled)
}

func TestNewClusterManager(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, manager)

	assert.NotNil(t, manager.cfg)
	assert.Equal(t, cfg, manager.cfg)
}

func TestClusterManagerStart(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start should not error (even though it's a no-op currently)
	err = manager.Start(ctx)
	assert.NoError(t, err)
}

func TestClusterManagerStop(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	// Stop should not error (even though it's a no-op currently)
	err = manager.Stop()
	assert.NoError(t, err)
}

func TestClusterManagerStartStop(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test multiple start/stop cycles
	for i := 0; i < 3; i++ {
		err = manager.Start(ctx)
		require.NoError(t, err, "Failed to start on iteration %d", i)

		err = manager.Stop()
		require.NoError(t, err, "Failed to stop on iteration %d", i)
	}
}

func TestClusterManagerWithDisabledCluster(t *testing.T) {
	cfg := createTestClusterConfig()
	cfg.Cluster.Enabled = false
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should work even when clustering is disabled
	err = manager.Start(ctx)
	assert.NoError(t, err)

	err = manager.Stop()
	assert.NoError(t, err)
}

func TestClusterManagerWithEmptyConfig(t *testing.T) {
	cfg := &config.Config{
		Cluster: config.ClusterConfig{
			Enabled: false,
		},
	}
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, manager)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	assert.NoError(t, err)

	err = manager.Stop()
	assert.NoError(t, err)
}

func TestClusterManagerConcurrentStartStop(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test concurrent operations
	done := make(chan error, 4)

	// Start operations
	go func() {
		done <- manager.Start(ctx)
	}()

	go func() {
		time.Sleep(10 * time.Millisecond)
		done <- manager.Start(ctx)
	}()

	// Stop operations
	go func() {
		time.Sleep(20 * time.Millisecond)
		done <- manager.Stop()
	}()

	go func() {
		time.Sleep(30 * time.Millisecond)
		done <- manager.Stop()
	}()

	// Wait for all operations to complete
	errors := make([]error, 0, 4)
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			errors = append(errors, err)
		}
	}

	// All operations should succeed (they're no-ops currently)
	assert.Empty(t, errors)
}

func TestClusterManagerContextCancellation(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Wait for context to be cancelled
	<-ctx.Done()

	// Stop should still work
	err = manager.Stop()
	assert.NoError(t, err)
}

func TestClusterManagerLoggerIntegration(t *testing.T) {
	cfg := createTestClusterConfig()
	
	// Use a logger that actually outputs (for this test only)
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// These operations should log messages (though we can't easily test the output)
	err = manager.Start(ctx)
	assert.NoError(t, err)

	err = manager.Stop()
	assert.NoError(t, err)
}

func TestClusterManagerWithVariousConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*config.Config)
	}{
		{
			name: "EmptyNodeID",
			modify: func(cfg *config.Config) {
				cfg.Cluster.NodeID = ""
			},
		},
		{
			name: "EmptyBind",
			modify: func(cfg *config.Config) {
				cfg.Cluster.Bind = ""
			},
		},
		{
			name: "NoPeers",
			modify: func(cfg *config.Config) {
				cfg.Cluster.Peers = []string{}
			},
		},
		{
			name: "ManyPeers",
			modify: func(cfg *config.Config) {
				cfg.Cluster.Peers = []string{
					"peer1:7946", "peer2:7946", "peer3:7946",
					"peer4:7946", "peer5:7946", "peer6:7946",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createTestClusterConfig()
			tt.modify(cfg)
			logger := createTestLogger()

			manager, err := New(cfg, logger)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			assert.NoError(t, err)

			err = manager.Stop()
			assert.NoError(t, err)
		})
	}
}

func TestClusterManagerNilConfig(t *testing.T) {
	logger := createTestLogger()

	// This should not panic, even with nil config
	assert.NotPanics(t, func() {
		manager, err := New(nil, logger)
		// May or may not error, depending on implementation
		// but should not panic
		_ = manager
		_ = err
	})
}

func TestClusterManagerStopWithoutStart(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	// Stop without start should not error
	err = manager.Stop()
	assert.NoError(t, err)
}

func TestClusterManagerMultipleStops(t *testing.T) {
	cfg := createTestClusterConfig()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Multiple stops should not error
	for i := 0; i < 3; i++ {
		err = manager.Stop()
		assert.NoError(t, err, "Stop failed on iteration %d", i)
	}
}