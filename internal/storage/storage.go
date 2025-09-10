package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/rs/zerolog"

	"github.com/x0a1b/promptmq/internal/config"
)

// buildSQLiteDSN constructs a SQLite DSN with configurable PRAGMA parameters
func buildSQLiteDSN(dbPath string, sqliteCfg *config.SQLiteConfig) string {
	foreignKeys := "OFF"
	if sqliteCfg.ForeignKeys {
		foreignKeys = "ON"
	}

	return fmt.Sprintf("%s?_journal_mode=%s&_synchronous=%s&_cache_size=%d&_foreign_keys=%s&_busy_timeout=%d&_temp_store=%s&_mmap_size=%d",
		dbPath,
		sqliteCfg.JournalMode,
		sqliteCfg.Synchronous,
		sqliteCfg.CacheSize,
		foreignKeys,
		sqliteCfg.BusyTimeout,
		sqliteCfg.TempStore,
		sqliteCfg.MmapSize,
	)
}

// Message represents a persisted MQTT message
type Message struct {
	ID        uint64    `json:"id"`
	Topic     string    `json:"topic"`
	Payload   []byte    `json:"payload"`
	QoS       byte      `json:"qos"`
	Retain    bool      `json:"retain"`
	ClientID  string    `json:"client_id"`
	Timestamp time.Time `json:"timestamp"`
}

// StorageMetrics defines the interface for storage metrics recording
type StorageMetrics interface {
	RecordSQLiteWrite(latency time.Duration, success bool)
	RecordStorageUsage(bytes int64)
	RecordRecovery(duration time.Duration)
}

// NoOpMetrics is a no-op implementation for when metrics are disabled
type NoOpMetrics struct{}

func (n *NoOpMetrics) RecordSQLiteWrite(time.Duration, bool) {}
func (n *NoOpMetrics) RecordStorageUsage(int64)             {}
func (n *NoOpMetrics) RecordRecovery(time.Duration)         {}

// Manager implements SQLite-based persistence for MQTT messages
type Manager struct {
	cfg             *config.Config
	logger          zerolog.Logger
	db              *sql.DB
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	messageID       atomic.Uint64
	totalMessages   atomic.Uint64
	totalBytes      atomic.Uint64
	metrics         StorageMetrics // For SQLite performance metrics
}


// New creates a new storage manager
func New(cfg *config.Config, logger zerolog.Logger) (*Manager, error) {
	return NewWithMetrics(cfg, logger, &NoOpMetrics{})
}

// NewWithMetrics creates a new storage manager with metrics integration
func NewWithMetrics(cfg *config.Config, logger zerolog.Logger, metrics StorageMetrics) (*Manager, error) {
	// Create data directory
	if err := os.MkdirAll(cfg.Storage.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Open SQLite database with configurable PRAGMA parameters for optimal performance
	dbPath := filepath.Join(cfg.Storage.DataDir, "promptmq.db")
	dsn := buildSQLiteDSN(dbPath, &cfg.Storage.SQLite)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// Set connection pool settings for better performance
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping SQLite database: %w", err)
	}

	// Create database tables
	if err := createTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create database tables: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	m := &Manager{
		cfg:     cfg,
		logger:  logger.With().Str("component", "storage").Logger(),
		db:      db,
		ctx:     ctx,
		cancel:  cancel,
		metrics: metrics,
	}

	return m, nil
}

// Start initializes the storage manager and starts background processes
func (m *Manager) Start(ctx context.Context) error {
	m.logger.Info().
		Str("data_dir", m.cfg.Storage.DataDir).
		Msg("Starting SQLite storage manager")

	// Recover retained messages from SQLite
	if err := m.recoverRetainedMessages(); err != nil {
		return fmt.Errorf("failed to recover retained messages: %w", err)
	}

	// Start metrics reporting loop
	m.wg.Add(1)
	go m.metricsLoop()

	m.logger.Info().Msg("SQLite storage manager started")
	return nil
}

// StopManager gracefully shuts down the storage manager
func (m *Manager) StopManager() error {
	m.logger.Info().Msg("Stopping SQLite storage manager")

	// Cancel context to stop background routines
	m.cancel()

	// Wait for background goroutines to finish
	m.wg.Wait()

	// Close SQLite database
	if err := m.db.Close(); err != nil {
		m.logger.Error().Err(err).Msg("Failed to close SQLite database")
		return err
	}

	m.logger.Info().Msg("SQLite database closed")

	m.logger.Info().
		Uint64("total_messages", m.totalMessages.Load()).
		Uint64("total_bytes", m.totalBytes.Load()).
		Msg("SQLite storage manager stopped")

	return nil
}

// persistMessage persists a message to SQLite (for all messages now, not just retained)
func (m *Manager) persistMessage(msg *Message) error {
	startTime := time.Now()

	// Insert into messages table
	insertSQL := `INSERT INTO messages (topic, payload, qos, retain, client_id, timestamp) VALUES (?, ?, ?, ?, ?, ?)`
	
	_, err := m.db.Exec(insertSQL, msg.Topic, msg.Payload, msg.QoS, msg.Retain, msg.ClientID, msg.Timestamp.UnixNano())
	writeLatency := time.Since(startTime)
	
	if err != nil {
		m.metrics.RecordSQLiteWrite(writeLatency, false)
		m.logger.Error().
			Err(err).
			Str("topic", msg.Topic).
			Str("client_id", msg.ClientID).
			Msg("Failed to persist message to SQLite")
		return fmt.Errorf("failed to persist message to SQLite: %w", err)
	}

	// Record successful write metrics
	m.metrics.RecordSQLiteWrite(writeLatency, true)

	// Update statistics
	m.totalMessages.Add(1)
	m.totalBytes.Add(uint64(len(msg.Payload) + len(msg.Topic) + len(msg.ClientID)))

	m.logger.Debug().
		Str("topic", msg.Topic).
		Str("client_id", msg.ClientID).
		Int("payload_size", len(msg.Payload)).
		Bool("retain", msg.Retain).
		Msg("Message persisted to SQLite")

	return nil
}


// createTables creates the SQLite tables for message storage
func createTables(db *sql.DB) error {
	// Create retained messages table
	retainedSQL := `
	CREATE TABLE IF NOT EXISTS retained_messages (
		topic TEXT PRIMARY KEY,
		payload BLOB,
		qos INTEGER,
		client_id TEXT,
		timestamp INTEGER,
		msg_id INTEGER
	);
	`
	
	if _, err := db.Exec(retainedSQL); err != nil {
		return fmt.Errorf("failed to create retained_messages table: %w", err)
	}

	// Create messages table for all message storage
	messagesSQL := `
	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		topic TEXT NOT NULL,
		payload BLOB,
		qos INTEGER NOT NULL,
		retain BOOLEAN NOT NULL,
		client_id TEXT NOT NULL,
		timestamp INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_messages_topic ON messages(topic);
	CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);
	`
	
	if _, err := db.Exec(messagesSQL); err != nil {
		return fmt.Errorf("failed to create messages table: %w", err)
	}
	
	return nil
}





// MQTT Hook Interface Implementation

// ID returns the hook identifier
func (m *Manager) ID() string {
	return "wal-storage"
}

// Provides indicates which hook methods this hook provides
func (m *Manager) Provides(b byte) bool {
	// Only provide the hooks we actually need for storage functionality
	switch b {
	case mqtt.OnConnect, mqtt.OnDisconnect, mqtt.OnPublish, mqtt.OnPublished, mqtt.OnRetainMessage, mqtt.OnRetainPublished, mqtt.StoredRetainedMessages:
		return true
	default:
		return false
	}
}

// Init initializes the hook with configuration
func (m *Manager) Init(config any) error {
	m.logger.Debug().Msg("Initializing storage hook")
	return nil
}

// OnConnectAuthenticate is called to authenticate a connecting client
func (m *Manager) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	// Storage manager doesn't handle authentication, allow all
	return true
}

// OnConnect is called when a client connects
func (m *Manager) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client connected")
	return nil
}

// OnDisconnect is called when a client disconnects
func (m *Manager) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	m.logger.Debug().Str("client_id", cl.ID).Bool("expire", expire).Msg("Client disconnected")
}

// OnPublish is called when a message is published (before processing)
func (m *Manager) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	if pk.FixedHeader.Type != packets.Publish {
		m.logger.Debug().Uint8("packet_type", uint8(pk.FixedHeader.Type)).Uint8("expected", uint8(packets.Publish)).Msg("Packet type mismatch in OnPublish")
		return pk, nil
	}

	// Create message from packet
	msg := &Message{
		ID:        m.messageID.Add(1),
		Topic:     pk.TopicName,
		Payload:   pk.Payload,
		QoS:       pk.FixedHeader.Qos,
		Retain:    pk.FixedHeader.Retain,
		ClientID:  cl.ID,
		Timestamp: time.Now(),
	}

	// Persist the message to SQLite
	if err := m.persistMessage(msg); err != nil {
		m.logger.Error().Err(err).Str("topic", pk.TopicName).Msg("Failed to persist message to SQLite")
		return pk, fmt.Errorf("failed to persist message to SQLite: %w", err)
	}

	m.logger.Debug().
		Uint64("msg_id", msg.ID).
		Str("topic", pk.TopicName).
		Int("payload_size", len(pk.Payload)).
		Str("client_id", cl.ID).
		Bool("retain", pk.FixedHeader.Retain).
		Msg("Message persisted to SQLite")

	return pk, nil
}

// OnPublished is called after a message has been published
func (m *Manager) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("topic", pk.TopicName).Str("client_id", cl.ID).Msg("Message published")
}

// OnACLCheck is called to check ACL permissions (we don't handle ACL in storage)
func (m *Manager) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	// Storage manager doesn't handle ACL, always return true (allow)
	return true
}

// Storage Query Methods

// GetMessagesByTopic retrieves messages for a specific topic from SQLite
func (m *Manager) GetMessagesByTopic(topic string, limit int) ([]*Message, error) {
	querySQL := `SELECT id, topic, payload, qos, retain, client_id, timestamp FROM messages WHERE topic = ? ORDER BY id DESC LIMIT ?`
	
	rows, err := m.db.Query(querySQL, topic, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages by topic: %w", err)
	}
	defer rows.Close()
	
	var messages []*Message
	for rows.Next() {
		var msg Message
		var timestampNano int64
		
		err := rows.Scan(&msg.ID, &msg.Topic, &msg.Payload, &msg.QoS, &msg.Retain, &msg.ClientID, &timestampNano)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message row: %w", err)
		}
		
		msg.Timestamp = time.Unix(0, timestampNano)
		messages = append(messages, &msg)
	}
	
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error during message query iteration: %w", err)
	}
	
	return messages, nil
}

// GetStats returns storage statistics
func (m *Manager) GetStats() map[string]interface{} {
	// Count unique topics from SQLite
	topicCountSQL := `SELECT COUNT(DISTINCT topic) FROM messages`
	var topicCount int
	if err := m.db.QueryRow(topicCountSQL).Scan(&topicCount); err != nil {
		m.logger.Error().Err(err).Msg("Failed to get topic count from SQLite")
		topicCount = 0
	}

	// Get database file size
	var dbSize int64
	dbSizeSQL := `SELECT page_count * page_size as size FROM pragma_page_count(), pragma_page_size()`
	if err := m.db.QueryRow(dbSizeSQL).Scan(&dbSize); err != nil {
		m.logger.Error().Err(err).Msg("Failed to get database size")
		dbSize = 0
	}

	// Count retained messages from SQLite
	var retainedCount int
	retainedSQL := `SELECT COUNT(*) FROM retained_messages`
	if err := m.db.QueryRow(retainedSQL).Scan(&retainedCount); err != nil {
		m.logger.Error().Err(err).Msg("Failed to get retained message count")
		retainedCount = 0
	}

	stats := map[string]interface{}{
		"total_messages":    m.totalMessages.Load(),
		"total_bytes":       m.totalBytes.Load(),
		"topic_count":       topicCount,
		"retained_count":    retainedCount,
		"next_message_id":   m.messageID.Load() + 1,
		"database_size":     dbSize,
		"storage_type":      "SQLite with WAL mode",
		"journal_mode":      "WAL",
		"synchronous":       "NORMAL",
		"cache_size":        "50MB",
		"mmap_size":         "256MB",
	}

	return stats
}

// CompactTopic removes old messages for a topic (SQLite-based cleanup)
func (m *Manager) CompactTopic(topic string, beforeTimestamp time.Time) error {
	deleteSQL := `DELETE FROM messages WHERE topic = ? AND timestamp < ?`
	
	result, err := m.db.Exec(deleteSQL, topic, beforeTimestamp.UnixNano())
	if err != nil {
		return fmt.Errorf("failed to compact topic %s: %w", topic, err)
	}
	
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected during compaction: %w", err)
	}

	m.logger.Info().
		Str("topic", topic).
		Time("before", beforeTimestamp).
		Int64("deleted_messages", rowsAffected).
		Msg("Topic compaction completed")

	return nil
}

// ForceFlush forces SQLite to sync all data to disk
func (m *Manager) ForceFlush() error {
	m.logger.Info().Msg("Force syncing SQLite database to disk")

	// Execute PRAGMA wal_checkpoint(FULL) to force WAL checkpoint
	if _, err := m.db.Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
		return fmt.Errorf("failed to checkpoint WAL: %w", err)
	}

	m.logger.Info().Msg("SQLite database sync completed")
	return nil
}

// metricsLoop periodically reports storage metrics
func (m *Manager) metricsLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(5 * time.Second) // Report metrics every 5 seconds
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reportMetrics()
		}
	}
}

// reportMetrics reports current storage metrics
func (m *Manager) reportMetrics() {
	// Get database size for storage usage metrics
	var dbSize int64
	dbSizeSQL := `SELECT page_count * page_size as size FROM pragma_page_count(), pragma_page_size()`
	if err := m.db.QueryRow(dbSizeSQL).Scan(&dbSize); err != nil {
		m.logger.Error().Err(err).Msg("Failed to get database size for metrics")
		dbSize = 0
	}

	// Report storage usage
	m.metrics.RecordStorageUsage(dbSize)
}

// persistRetainedToDB stores a retained message to SQLite for persistence across restarts
func (m *Manager) persistRetainedToDB(msg *Message) error {
	insertSQL := `INSERT OR REPLACE INTO retained_messages (topic, payload, qos, client_id, timestamp, msg_id) VALUES (?, ?, ?, ?, ?, ?)`
	
	_, err := m.db.Exec(insertSQL, msg.Topic, msg.Payload, msg.QoS, msg.ClientID, msg.Timestamp.UnixNano(), msg.ID)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("topic", msg.Topic).
			Str("client_id", msg.ClientID).
			Msg("Failed to persist retained message to SQLite")
		return fmt.Errorf("failed to persist retained message to SQLite: %w", err)
	}
	
	m.logger.Debug().
		Str("topic", msg.Topic).
		Str("client_id", msg.ClientID).
		Int("payload_size", len(msg.Payload)).
		Uint64("msg_id", msg.ID).
		Msg("Retained message persisted to SQLite")
	
	// Immediately verify the write by reading it back
	var count int
	verifySQL := `SELECT COUNT(*) FROM retained_messages WHERE topic = ?`
	err = m.db.QueryRow(verifySQL, msg.Topic).Scan(&count)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("topic", msg.Topic).
			Msg("Failed to verify retained message write to SQLite")
		return fmt.Errorf("failed to verify retained message write: %w", err)
	}
	
	if count != 1 {
		m.logger.Error().
			Str("topic", msg.Topic).
			Int("count", count).
			Msg("Unexpected count after retained message write")
		return fmt.Errorf("expected count 1, got %d for topic %s", count, msg.Topic)
	}
	
	m.logger.Debug().
		Str("topic", msg.Topic).
		Msg("Verified retained message write to SQLite")
	
	return nil
}

// deleteRetainedFromDB removes a retained message from SQLite
func (m *Manager) deleteRetainedFromDB(topic string) error {
	deleteSQL := `DELETE FROM retained_messages WHERE topic = ?`
	
	result, err := m.db.Exec(deleteSQL, topic)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("topic", topic).
			Msg("Failed to delete retained message from SQLite")
		return fmt.Errorf("failed to delete retained message from SQLite: %w", err)
	}
	
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("topic", topic).
			Msg("Failed to get rows affected for retained message deletion")
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	
	m.logger.Debug().
		Str("topic", topic).
		Int64("rows_affected", rowsAffected).
		Msg("Retained message deleted from SQLite")
	
	return nil
}

// recoverRetainedMessages recovers retained messages from SQLite on startup
func (m *Manager) recoverRetainedMessages() error {
	m.logger.Info().
		Str("data_dir", m.cfg.Storage.DataDir).
		Msg("Recovering retained messages from SQLite")
	
	// Also recover message statistics from the messages table
	recoveryStart := time.Now()
	var maxID, messageCount uint64
	
	// Get max message ID and total message count for proper initialization
	statsSQL := `SELECT COALESCE(MAX(id), 0), COUNT(*) FROM messages`
	var maxIDInt64, messageCountInt64 int64
	err := m.db.QueryRow(statsSQL).Scan(&maxIDInt64, &messageCountInt64)
	if err != nil {
		m.logger.Error().Err(err).Msg("Failed to recover message statistics")
	} else {
		maxID = uint64(maxIDInt64)
		messageCount = uint64(messageCountInt64)
		
		// Update counters
		m.messageID.Store(maxID)
		m.totalMessages.Store(messageCount)
		
		m.logger.Info().
			Uint64("max_message_id", maxID).
			Uint64("total_messages", messageCount).
			Msg("Recovered message statistics from SQLite")
	}
	
	// Count total retained messages
	var totalCount int
	countSQL := `SELECT COUNT(*) FROM retained_messages`
	err = m.db.QueryRow(countSQL).Scan(&totalCount)
	if err != nil {
		return fmt.Errorf("failed to count retained messages: %w", err)
	}
	
	m.logger.Info().
		Int("total_retained_messages", totalCount).
		Msg("SQLite retained message count completed")
	
	// SQLite stores retained messages persistently, no need to load into memory
	// Just validate the database is accessible and log the count
	var recovered int
	if totalCount > 0 {
		recovered = totalCount
		m.logger.Debug().Int("retained_count", recovered).Msg("Retained messages available in SQLite")
	}
	
	recoveryDuration := time.Since(recoveryStart)
	m.metrics.RecordRecovery(recoveryDuration)
	
	m.logger.Info().
		Int("recovered_count", recovered).
		Dur("recovery_duration", recoveryDuration).
		Msg("Retained message recovery completed")
	
	return nil
}
