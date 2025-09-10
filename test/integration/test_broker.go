package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/x0a1b/promptmq/internal/broker"
	"github.com/x0a1b/promptmq/internal/config"
)

// TestBroker provides a reliable, isolated MQTT broker for integration testing
type TestBroker struct {
	broker      *broker.Broker
	ctx         context.Context
	cancel      context.CancelFunc
	cfg         *config.Config
	tempDir     string
	mqttPort    int
	metricsPort int
	wsPort      int
	t           *testing.T
}

// TestBrokerConfig holds configuration options for test broker
type TestBrokerConfig struct {
	SQLiteConfig *config.SQLiteConfig
	LogLevel     string
	EnableMetrics bool
}

// DefaultTestConfig returns a default test configuration
func DefaultTestConfig() *TestBrokerConfig {
	return &TestBrokerConfig{
		SQLiteConfig: &config.SQLiteConfig{
			CacheSize:    1000,     // Small cache for tests
			TempStore:    "MEMORY", // Use memory for temp storage
			MmapSize:     1048576,  // 1MB mmap
			BusyTimeout:  5000,     // 5 second timeout
			Synchronous:  "NORMAL", // Balanced mode
			JournalMode:  "WAL",    // WAL mode
			ForeignKeys:  true,     // Enable constraints
		},
		LogLevel:      "error", // Reduce noise in tests
		EnableMetrics: true,
	}
}

// NewTestBroker creates a new isolated test broker instance
func NewTestBroker(t *testing.T, testConfig *TestBrokerConfig) *TestBroker {
	if testConfig == nil {
		testConfig = DefaultTestConfig()
	}

	// Create temporary directory for this test
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	
	err := os.MkdirAll(dataDir, 0755)
	require.NoError(t, err, "Failed to create test data directory")

	// Allocate available ports
	mqttPort, err := getAvailablePort()
	require.NoError(t, err, "Failed to allocate MQTT port")
	
	metricsPort, err := getAvailablePort()
	require.NoError(t, err, "Failed to allocate metrics port")
	
	wsPort, err := getAvailablePort()
	require.NoError(t, err, "Failed to allocate WebSocket port")

	// Create test configuration
	cfg := &config.Config{
		Log: config.LogConfig{
			Level:  testConfig.LogLevel,
			Format: "console", // Easier to read in tests
		},
		Server: config.ServerConfig{
			Bind:         fmt.Sprintf("127.0.0.1:%d", mqttPort),
			WSBind:       fmt.Sprintf("127.0.0.1:%d", wsPort),
			ReadTimeout:  "10s",
			WriteTimeout: "10s",
			ReadBuffer:   4096,
			WriteBuffer:  4096,
		},
		MQTT: config.MQTTConfig{
			MaxQoS:               2,
			MaxConnections:       1000,
			MaxInflight:          20,
			KeepAlive:            60,
			RetainAvailable:      true,
			WildcardAvailable:    true,
			SharedSubAvailable:   true,
			MaxPacketSize:        65535,
			MaxClientIDLen:       65535,
			MaxTopicLen:          65535,
		},
		Storage: config.StorageConfig{
			DataDir: dataDir,
			SQLite:  *testConfig.SQLiteConfig,
			Cleanup: config.CleanupConfig{
				MaxMessageAge: 24 * time.Hour,
				CheckInterval: time.Hour,
				BatchSize:     100,
			},
		},
		Metrics: config.MetricsConfig{
			Enabled: testConfig.EnableMetrics,
			Bind:    fmt.Sprintf("127.0.0.1:%d", metricsPort),
			Path:    "/metrics",
		},
		Cluster: config.ClusterConfig{
			Enabled: false, // No clustering in tests
		},
	}

	// Create context for the test broker
	ctx, cancel := context.WithCancel(context.Background())

	tb := &TestBroker{
		ctx:         ctx,
		cancel:      cancel,
		cfg:         cfg,
		tempDir:     tempDir,
		mqttPort:    mqttPort,
		metricsPort: metricsPort,
		wsPort:      wsPort,
		t:           t,
	}

	// Ensure cleanup happens when test ends
	t.Cleanup(func() {
		tb.Stop()
	})

	return tb
}

// Start starts the test broker and waits for it to be ready
func (tb *TestBroker) Start() error {
	// Create broker instance
	var err error
	tb.broker, err = broker.New(tb.cfg)
	if err != nil {
		return fmt.Errorf("failed to create broker: %w", err)
	}

	// Start broker in background
	brokerReady := make(chan error, 1)
	go func() {
		err := tb.broker.Start(tb.ctx)
		if err != nil && err != context.Canceled {
			brokerReady <- err
			return
		}
		close(brokerReady)
	}()

	// Wait for broker to start or timeout
	select {
	case err := <-brokerReady:
		if err != nil {
			return fmt.Errorf("broker failed to start: %w", err)
		}
	case <-time.After(10 * time.Second):
		tb.cancel()
		return fmt.Errorf("broker failed to start within timeout")
	}

	// Wait for MQTT port to be available
	if err := waitForPort(tb.mqttPort, 5*time.Second); err != nil {
		tb.cancel()
		return fmt.Errorf("MQTT port not ready: %w", err)
	}

	// Wait for metrics port if enabled
	if tb.cfg.Metrics.Enabled {
		if err := waitForPort(tb.metricsPort, 5*time.Second); err != nil {
			tb.cancel()
			return fmt.Errorf("metrics port not ready: %w", err)
		}
	}

	return nil
}

// Stop stops the test broker and cleans up resources
func (tb *TestBroker) Stop() {
	if tb.cancel != nil {
		tb.cancel()
	}

	// Give broker time to shutdown gracefully
	time.Sleep(100 * time.Millisecond)
}

// MQTTAddress returns the MQTT broker address for client connections
func (tb *TestBroker) MQTTAddress() string {
	return fmt.Sprintf("tcp://127.0.0.1:%d", tb.mqttPort)
}

// MetricsAddress returns the metrics HTTP address
func (tb *TestBroker) MetricsAddress() string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", tb.metricsPort, tb.cfg.Metrics.Path)
}

// WebSocketAddress returns the WebSocket address
func (tb *TestBroker) WebSocketAddress() string {
	return fmt.Sprintf("ws://127.0.0.1:%d", tb.wsPort)
}

// Config returns the broker configuration
func (tb *TestBroker) Config() *config.Config {
	return tb.cfg
}

// TempDir returns the temporary directory for this test
func (tb *TestBroker) TempDir() string {
	return tb.tempDir
}

// getAvailablePort finds an available port on the system
func getAvailablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port, nil
}

// waitForPort waits for a port to become available for connections
func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	
	return fmt.Errorf("port %d not available within timeout", port)
}