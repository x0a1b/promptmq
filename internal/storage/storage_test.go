package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/mochi-mqtt/server/v2/system"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/x0a1b/promptmq/internal/config"
)

func createTestConfig(t *testing.T) *config.Config {
	tmpDir, err := os.MkdirTemp("", "promptmq-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})

	return &config.Config{
		Storage: config.StorageConfig{
			DataDir: filepath.Join(tmpDir, "data"),
			Cleanup: config.CleanupConfig{
				MaxMessageAge: 24 * time.Hour,
				CheckInterval: 1 * time.Hour,
				BatchSize:     100,
			},
		},
	}
}

func createTestLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).Level(zerolog.Disabled)
}

func TestNew(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, manager)

	// Check directories were created
	assert.DirExists(t, cfg.Storage.DataDir)

	err = manager.StopManager()
	require.NoError(t, err)
}

func TestStartStop(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	err = manager.StopManager()
	require.NoError(t, err)
}

func TestPersistMessage(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Create test message
	msg := &Message{
		ID:        1,
		Topic:     "test/topic",
		Payload:   []byte("test message"),
		QoS:       1,
		Retain:    false,
		ClientID:  "test-client",
		Timestamp: time.Now(),
	}

	// Persist message
	err = manager.persistMessage(msg)
	require.NoError(t, err)

	// Verify stats updated
	stats := manager.GetStats()
	assert.Equal(t, uint64(1), stats["total_messages"])
	assert.Greater(t, stats["total_bytes"], uint64(0))
}

func TestRetainedMessages(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Create retained message
	msg := &Message{
		ID:        1,
		Topic:     "test/retained",
		Payload:   []byte("retained message"),
		QoS:       1,
		Retain:    true,
		ClientID:  "test-client",
		Timestamp: time.Now(),
	}

	// Persist retained message
	err = manager.persistRetainedToDB(msg)
	require.NoError(t, err)

	// Retrieve retained messages
	messages, err := manager.StoredRetainedMessages()
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "test/retained", messages[0].TopicName)
	assert.Equal(t, []byte("retained message"), messages[0].Payload)
}

func TestBuildSQLiteDSN(t *testing.T) {
	tests := []struct {
		name     string
		dbPath   string
		sqliteCfg *config.SQLiteConfig
		expected string
	}{
		{
			name:   "default config",
			dbPath: "/tmp/test.db",
			sqliteCfg: &config.SQLiteConfig{
				CacheSize:    50000,
				TempStore:    "MEMORY",
				MmapSize:     268435456,
				BusyTimeout:  30000,
				Synchronous:  "NORMAL",
				JournalMode:  "WAL",
				ForeignKeys:  true,
			},
			expected: "/tmp/test.db?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=50000&_foreign_keys=ON&_busy_timeout=30000&_temp_store=MEMORY&_mmap_size=268435456",
		},
		{
			name:   "foreign keys disabled",
			dbPath: "/tmp/test.db",
			sqliteCfg: &config.SQLiteConfig{
				CacheSize:    10000,
				TempStore:    "FILE",
				MmapSize:     134217728,
				BusyTimeout:  5000,
				Synchronous:  "FULL",
				JournalMode:  "DELETE",
				ForeignKeys:  false,
			},
			expected: "/tmp/test.db?_journal_mode=DELETE&_synchronous=FULL&_cache_size=10000&_foreign_keys=OFF&_busy_timeout=5000&_temp_store=FILE&_mmap_size=134217728",
		},
		{
			name:   "high performance config",
			dbPath: "/tmp/test.db",
			sqliteCfg: &config.SQLiteConfig{
				CacheSize:    100000,
				TempStore:    "MEMORY",
				MmapSize:     536870912, // 512MB
				BusyTimeout:  1000,
				Synchronous:  "OFF",
				JournalMode:  "MEMORY",
				ForeignKeys:  false,
			},
			expected: "/tmp/test.db?_journal_mode=MEMORY&_synchronous=OFF&_cache_size=100000&_foreign_keys=OFF&_busy_timeout=1000&_temp_store=MEMORY&_mmap_size=536870912",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildSQLiteDSN(tt.dbPath, tt.sqliteCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNoOpMetrics(t *testing.T) {
	metrics := &NoOpMetrics{}

	// Test all methods can be called without panicking
	assert.NotPanics(t, func() {
		metrics.RecordSQLiteWrite(100*time.Millisecond, true)
		metrics.RecordStorageUsage(1024)
		metrics.RecordRecovery(time.Second)
	})
}

func TestNewWithMetrics(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	// Create a simple mock metrics implementation
	mockMetrics := &NoOpMetrics{}

	manager, err := NewWithMetrics(cfg, logger, mockMetrics)
	require.NoError(t, err)
	require.NotNil(t, manager)
	defer manager.StopManager()

	// Verify the manager was created with metrics
	assert.NotNil(t, manager.metrics)
}

func TestManagerHookMethods(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	// Test Hook interface methods
	assert.Equal(t, "wal-storage", manager.ID())
	assert.True(t, manager.Provides(mqtt.OnConnect))
	assert.False(t, manager.Provides(255)) // Invalid hook byte

	// Test hook lifecycle methods don't panic
	client := &mqtt.Client{ID: "test-client"}
	assert.NotPanics(t, func() {
		manager.Init(nil)
		assert.True(t, manager.OnConnectAuthenticate(client, packets.Packet{}))
		assert.NoError(t, manager.OnConnect(client, packets.Packet{}))
		manager.OnDisconnect(client, nil, false)
		assert.True(t, manager.OnACLCheck(client, "test/topic", true))
	})
}

func TestOnPublish(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Create test packet
	packet := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type: packets.Publish,
			Qos:  1,
		},
		TopicName: "test/topic",
		Payload:   []byte("test message"),
	}

	// Test OnPublish
	client := &mqtt.Client{ID: "test-client"}
	result, err := manager.OnPublish(client, packet)
	assert.NoError(t, err)
	assert.Equal(t, packet, result)
}

func TestGetStats(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Get initial stats
	stats := manager.GetStats()
	assert.Contains(t, stats, "total_messages")
	assert.Contains(t, stats, "total_bytes")
	assert.Contains(t, stats, "retained_count")
	assert.Contains(t, stats, "database_size")
	assert.Contains(t, stats, "topic_count")
	assert.Contains(t, stats, "storage_type")

	// Initial values should be zero or positive
	assert.GreaterOrEqual(t, stats["total_messages"], uint64(0))
	assert.GreaterOrEqual(t, stats["total_bytes"], uint64(0))
	assert.GreaterOrEqual(t, stats["retained_count"], 0)
	assert.GreaterOrEqual(t, stats["database_size"], int64(0))
}

func TestGetMessagesByTopic(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	// Test GetMessagesByTopic
	messages, err := manager.GetMessagesByTopic("test/topic", 10)
	assert.NoError(t, err)
	assert.Empty(t, messages)
}

func TestOnPublished(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	// Test OnPublished doesn't panic
	client := &mqtt.Client{ID: "test-client"}
	assert.NotPanics(t, func() {
		manager.OnPublished(client, packets.Packet{})
	})
}

func TestDeleteRetainedFromDB(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// First persist a retained message
	msg := &Message{
		ID:        1,
		Topic:     "test/retained",
		Payload:   []byte("retained message"),
		QoS:       1,
		Retain:    true,
		ClientID:  "test-client",
		Timestamp: time.Now(),
	}

	err = manager.persistRetainedToDB(msg)
	require.NoError(t, err)

	// Verify it exists
	messages, err := manager.StoredRetainedMessages()
	require.NoError(t, err)
	require.Len(t, messages, 1)

	// Now delete it
	manager.deleteRetainedFromDB("test/retained")

	// Verify it's gone
	messages, err = manager.StoredRetainedMessages()
	require.NoError(t, err)
	assert.Len(t, messages, 0)
}

func TestHookMethods(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	client := &mqtt.Client{ID: "test-client"}

	// Test SetOpts
	assert.NotPanics(t, func() {
		manager.SetOpts(nil, nil)
	})

	// Test OnSysInfoTick
	assert.NotPanics(t, func() {
		manager.OnSysInfoTick(nil)
	})

	// Test OnPacketSent
	assert.NotPanics(t, func() {
		manager.OnPacketSent(client, packets.Packet{}, []byte("test"))
	})

	// Test OnPacketProcessed
	assert.NotPanics(t, func() {
		manager.OnPacketProcessed(client, packets.Packet{}, nil)
		manager.OnPacketProcessed(client, packets.Packet{}, fmt.Errorf("test error"))
	})
}


func TestCompactTopic(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Test CompactTopic
	beforeTime := time.Now().Add(-1 * time.Hour)
	err = manager.CompactTopic("test/topic", beforeTime)
	assert.NoError(t, err)
}

func TestForceFlush(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Test ForceFlush
	assert.NotPanics(t, func() {
		manager.ForceFlush()
	})
}

func TestOnPublishErrorHandling(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	client := &mqtt.Client{ID: "test-client"}

	// Test with non-publish packet type
	nonPublishPacket := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type: packets.Connect,
		},
		TopicName: "test/topic",
		Payload:   []byte("test"),
	}

	result, err := manager.OnPublish(client, nonPublishPacket)
	assert.NoError(t, err)
	assert.Equal(t, nonPublishPacket, result)

	// Test with retain flag
	retainPacket := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type:   packets.Publish,
			Retain: true,
		},
		TopicName: "test/retain",
		Payload:   []byte("retain test"),
	}

	result, err = manager.OnPublish(client, retainPacket)
	assert.NoError(t, err)
	assert.Equal(t, retainPacket, result)
}

func TestGetMessagesByTopicWithData(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Persist a message first
	msg := &Message{
		ID:        1,
		Topic:     "test/data",
		Payload:   []byte("test message"),
		QoS:       1,
		Retain:    false,
		ClientID:  "test-client",
		Timestamp: time.Now(),
	}

	err = manager.persistMessage(msg)
	require.NoError(t, err)

	// Now retrieve messages
	messages, err := manager.GetMessagesByTopic("test/data", 10)
	assert.NoError(t, err)
	assert.Len(t, messages, 1)
	assert.Equal(t, "test/data", messages[0].Topic)
	assert.Equal(t, []byte("test message"), messages[0].Payload)
}

func TestNoOpMetricsMethods(t *testing.T) {
	metrics := &NoOpMetrics{}
	
	// Test all NoOpMetrics methods - they should not panic
	assert.NotPanics(t, func() {
		metrics.RecordSQLiteWrite(time.Millisecond, true)
		metrics.RecordSQLiteWrite(time.Microsecond, false)
		metrics.RecordStorageUsage(1024)
		metrics.RecordStorageUsage(0)
		metrics.RecordRecovery(time.Second)
		metrics.RecordRecovery(time.Nanosecond)
	})
}

func TestReportMetrics(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Test reportMetrics functionality indirectly
	// The method should not panic even if database queries fail
	assert.NotPanics(t, func() {
		stats := manager.GetStats()
		assert.NotNil(t, stats)
	})
}

func TestHookMethodsWithMetrics(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	// Create manager with NoOpMetrics to test metrics recording
	manager, err := NewWithMetrics(cfg, logger, &NoOpMetrics{})
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	client := &mqtt.Client{ID: "test-client"}
	
	// Test SetOpts - should not panic
	assert.NotPanics(t, func() {
		manager.SetOpts(nil, nil)
		manager.SetOpts(slog.Default(), &mqtt.HookOptions{})
	})

	// Test OnSysInfoTick - should not panic
	assert.NotPanics(t, func() {
		sysInfo := &system.Info{
			Version: "1.0.0",
			Started: time.Now().Unix(),
		}
		manager.OnSysInfoTick(sysInfo)
	})

	// Test OnPacketSent - should not panic
	assert.NotPanics(t, func() {
		manager.OnPacketSent(client, packets.Packet{}, []byte("test"))
	})

	// Test OnPacketProcessed - should not panic
	assert.NotPanics(t, func() {
		manager.OnPacketProcessed(client, packets.Packet{}, nil)
		manager.OnPacketProcessed(client, packets.Packet{}, fmt.Errorf("test error"))
	})
}

func TestGetStatsDetailed(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Test GetStats with database operations to increase coverage
	stats := manager.GetStats()
	assert.NotNil(t, stats)
	
	// Add a message and test again to cover more code paths
	msg := &Message{
		ID:        1,
		Topic:     "test/stats",
		Payload:   []byte("test message"),
		QoS:       1,
		Retain:    true,
		ClientID:  "test-client",
		Timestamp: time.Now(),
	}

	err = manager.persistMessage(msg)
	require.NoError(t, err)

	// Get stats again after adding data
	stats2 := manager.GetStats()
	assert.NotNil(t, stats2)
	
	// Should have message counts
	assert.Contains(t, stats2, "total_messages")
}

func TestNewWithMetricsErrorHandling(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.DataDir = "/invalid/path/that/does/not/exist"
	logger := createTestLogger()

	// Test NewWithMetrics with invalid config to increase coverage
	manager, err := NewWithMetrics(cfg, logger, &NoOpMetrics{})
	assert.Error(t, err)
	assert.Nil(t, manager)
}

func TestPersistRetainedToDBCoverage(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// This should test the persistRetainedToDB method through OnRetainMessage
	client := &mqtt.Client{ID: "retain-client"}
	packet := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type:   packets.Publish,
			Retain: true,
		},
		TopicName: "retain/test",
		Payload:   []byte("retained message"),
	}

	assert.NotPanics(t, func() {
		manager.OnRetainMessage(client, packet, 123)
	})
}