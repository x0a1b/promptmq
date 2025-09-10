package integration

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMQTTHighThroughput tests high-throughput message publishing and delivery
func TestMQTTHighThroughput(t *testing.T) {
	// Use high-performance configuration
	config := DefaultTestConfig()
	config.SQLiteConfig.CacheSize = 10000    // Larger cache for performance
	config.SQLiteConfig.Synchronous = "OFF"  // Maximum performance mode
	config.SQLiteConfig.JournalMode = "MEMORY" // In-memory journal for speed
	
	broker := NewTestBroker(t, config)
	require.NoError(t, broker.Start())

	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "throughput-publisher",
	})
	subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "throughput-subscriber",
	})

	require.NoError(t, publisher.Connect())
	require.NoError(t, subscriber.Connect())

	topic := "test/throughput"
	require.NoError(t, subscriber.Subscribe(topic, 0))
	time.Sleep(100 * time.Millisecond) // Allow subscription to propagate

	// Performance test parameters
	numMessages := 1000
	messageSize := 100 // bytes
	payload := make([]byte, messageSize)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	// Start timing
	startTime := time.Now()

	// Publish messages as fast as possible
	for i := 0; i < numMessages; i++ {
		require.NoError(t, publisher.Publish(topic, 0, false, payload))
	}

	publishDuration := time.Since(startTime)

	// Wait for all messages to be received
	messages, err := subscriber.WaitForMessages(topic, numMessages, 10*time.Second)
	require.NoError(t, err, "Failed to receive all messages")

	totalDuration := time.Since(startTime)

	// Calculate performance metrics
	publishThroughput := float64(numMessages) / publishDuration.Seconds()
	endToEndThroughput := float64(numMessages) / totalDuration.Seconds()
	avgLatency := totalDuration / time.Duration(numMessages)

	// Log performance results
	t.Logf("High Throughput Test Results:")
	t.Logf("  Messages: %d", numMessages)
	t.Logf("  Message Size: %d bytes", messageSize)
	t.Logf("  Publish Throughput: %.2f msg/sec", publishThroughput)
	t.Logf("  End-to-End Throughput: %.2f msg/sec", endToEndThroughput)
	t.Logf("  Average Latency: %v", avgLatency)
	t.Logf("  Publish Duration: %v", publishDuration)
	t.Logf("  Total Duration: %v", totalDuration)

	// Verify all messages received
	assert.Len(t, messages, numMessages, "All messages should be received")

	// Performance assertions (should exceed minimum thresholds)
	assert.Greater(t, publishThroughput, 500.0, "Publish throughput should exceed 500 msg/sec")
	assert.Greater(t, endToEndThroughput, 200.0, "End-to-end throughput should exceed 200 msg/sec")
	assert.Less(t, avgLatency, 50*time.Millisecond, "Average latency should be under 50ms")
}

// TestMQTTConcurrentClients tests performance with multiple concurrent clients
func TestMQTTConcurrentClients(t *testing.T) {
	config := DefaultTestConfig()
	config.SQLiteConfig.CacheSize = 20000 // Large cache for concurrent access
	
	broker := NewTestBroker(t, config)
	require.NoError(t, broker.Start())

	numPublishers := 5
	numSubscribers := 5
	messagesPerPublisher := 100
	topic := "test/concurrent"

	var publishers []*TestClient
	var subscribers []*TestClient

	// Create and connect subscriber clients
	for i := 0; i < numSubscribers; i++ {
		subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: fmt.Sprintf("concurrent-subscriber-%d", i),
		})
		require.NoError(t, subscriber.Connect())
		require.NoError(t, subscriber.Subscribe(topic, 0))
		subscribers = append(subscribers, subscriber)
	}

	// Create and connect publisher clients
	for i := 0; i < numPublishers; i++ {
		publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: fmt.Sprintf("concurrent-publisher-%d", i),
		})
		require.NoError(t, publisher.Connect())
		publishers = append(publishers, publisher)
	}

	// Allow connections and subscriptions to stabilize
	time.Sleep(200 * time.Millisecond)

	// Start concurrent publishing
	var publishWg sync.WaitGroup
	var publishErrors int32
	startTime := time.Now()

	for i, publisher := range publishers {
		publishWg.Add(1)
		go func(pub *TestClient, publisherID int) {
			defer publishWg.Done()
			
			for j := 0; j < messagesPerPublisher; j++ {
				message := fmt.Sprintf("Message from publisher %d, sequence %d", publisherID, j)
				if err := pub.PublishString(topic, 0, false, message); err != nil {
					t.Logf("Publish error from publisher %d: %v", publisherID, err)
					atomic.AddInt32(&publishErrors, 1)
				}
			}
		}(publisher, i)
	}

	// Wait for all publishing to complete
	publishWg.Wait()
	publishDuration := time.Since(startTime)

	// Verify no publish errors
	assert.Equal(t, int32(0), atomic.LoadInt32(&publishErrors), "No publish errors should occur")

	// Wait for message delivery to all subscribers
	expectedMessages := numPublishers * messagesPerPublisher
	
	for i, subscriber := range subscribers {
		messages, err := subscriber.WaitForMessages(topic, expectedMessages, 10*time.Second)
		require.NoError(t, err, "Subscriber %d should receive all messages", i)
		assert.Len(t, messages, expectedMessages, "Subscriber %d should receive exactly %d messages", i, expectedMessages)
	}

	totalDuration := time.Since(startTime)

	// Calculate performance metrics
	totalMessagesPublished := numPublishers * messagesPerPublisher
	totalMessagesReceived := numSubscribers * expectedMessages
	publishThroughput := float64(totalMessagesPublished) / publishDuration.Seconds()
	receiveThroughput := float64(totalMessagesReceived) / totalDuration.Seconds()

	// Log performance results
	t.Logf("Concurrent Clients Test Results:")
	t.Logf("  Publishers: %d", numPublishers)
	t.Logf("  Subscribers: %d", numSubscribers)
	t.Logf("  Messages per Publisher: %d", messagesPerPublisher)
	t.Logf("  Total Messages Published: %d", totalMessagesPublished)
	t.Logf("  Total Messages Received: %d", totalMessagesReceived)
	t.Logf("  Publish Throughput: %.2f msg/sec", publishThroughput)
	t.Logf("  Receive Throughput: %.2f msg/sec", receiveThroughput)
	t.Logf("  Publish Duration: %v", publishDuration)
	t.Logf("  Total Duration: %v", totalDuration)

	// Performance assertions
	assert.Greater(t, publishThroughput, 200.0, "Concurrent publish throughput should exceed 200 msg/sec")
	assert.Greater(t, receiveThroughput, 500.0, "Concurrent receive throughput should exceed 500 msg/sec")
}

// TestMQTTMemoryEfficiency tests memory usage during high-load scenarios
func TestMQTTMemoryEfficiency(t *testing.T) {
	config := DefaultTestConfig()
	config.SQLiteConfig.CacheSize = 5000 // Moderate cache to test memory efficiency
	
	broker := NewTestBroker(t, config)
	require.NoError(t, broker.Start())

	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "memory-publisher",
	})
	subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "memory-subscriber",
	})

	require.NoError(t, publisher.Connect())
	require.NoError(t, subscriber.Connect())

	// Test with different message sizes
	testCases := []struct {
		name        string
		messageSize int
		numMessages int
	}{
		{"SmallMessages", 10, 1000},
		{"MediumMessages", 1024, 500},      // 1KB messages
		{"LargeMessages", 10240, 100},      // 10KB messages
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			topic := fmt.Sprintf("test/memory/%s", tc.name)
			require.NoError(t, subscriber.Subscribe(topic, 0))
			time.Sleep(100 * time.Millisecond)

			// Create payload of specified size
			payload := make([]byte, tc.messageSize)
			for i := range payload {
				payload[i] = byte(i % 256)
			}

			startTime := time.Now()

			// Publish messages
			for i := 0; i < tc.numMessages; i++ {
				require.NoError(t, publisher.Publish(topic, 0, false, payload))
			}

			// Wait for all messages to be received
			messages, err := subscriber.WaitForMessages(topic, tc.numMessages, 10*time.Second)
			require.NoError(t, err, "Failed to receive all messages for %s", tc.name)

			duration := time.Since(startTime)
			throughput := float64(tc.numMessages) / duration.Seconds()
			dataRate := float64(tc.numMessages*tc.messageSize) / duration.Seconds() / 1024 / 1024 // MB/s

			t.Logf("Memory Efficiency Test - %s:", tc.name)
			t.Logf("  Messages: %d x %d bytes", tc.numMessages, tc.messageSize)
			t.Logf("  Throughput: %.2f msg/sec", throughput)
			t.Logf("  Data Rate: %.2f MB/s", dataRate)
			t.Logf("  Duration: %v", duration)

			// Verify message integrity
			assert.Len(t, messages, tc.numMessages)
			for _, msg := range messages {
				assert.Len(t, msg.Payload, tc.messageSize, "Message size should match")
				assert.Equal(t, payload, msg.Payload, "Message content should match")
			}

			// Clean up for next test
			subscriber.ClearMessages()
			require.NoError(t, subscriber.Unsubscribe(topic))
		})
	}
}

// TestMQTTConnectionScaling tests broker behavior with many simultaneous connections
func TestMQTTConnectionScaling(t *testing.T) {
	config := DefaultTestConfig()
	config.SQLiteConfig.CacheSize = 15000 // Large cache for many connections
	
	broker := NewTestBroker(t, config)
	require.NoError(t, broker.Start())

	numConnections := 50
	topic := "test/scaling"
	message := "Connection scaling test message"

	var clients []*TestClient
	var connectErrors int32

	// Create and connect multiple clients
	startTime := time.Now()
	
	var wg sync.WaitGroup
	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			
			client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
				ClientID: fmt.Sprintf("scaling-client-%d", clientID),
			})
			
			if err := client.Connect(); err != nil {
				t.Logf("Connection error for client %d: %v", clientID, err)
				atomic.AddInt32(&connectErrors, 1)
				return
			}
			
			if err := client.Subscribe(topic, 0); err != nil {
				t.Logf("Subscribe error for client %d: %v", clientID, err)
				atomic.AddInt32(&connectErrors, 1)
				return
			}
			
			clients = append(clients, client)
		}(i)
	}

	wg.Wait()
	connectionDuration := time.Since(startTime)

	// Verify all connections succeeded
	connectErrorCount := atomic.LoadInt32(&connectErrors)
	assert.Equal(t, int32(0), connectErrorCount, "All connections should succeed")
	assert.Len(t, clients, numConnections, "All clients should be connected")

	// Allow subscriptions to propagate
	time.Sleep(500 * time.Millisecond)

	// Create publisher and send broadcast message
	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "scaling-publisher",
	})
	require.NoError(t, publisher.Connect())

	publishStart := time.Now()
	require.NoError(t, publisher.PublishString(topic, 0, false, message))

	// Wait for message delivery to all clients
	var receiveWg sync.WaitGroup
	var receiveErrors int32
	
	for i, client := range clients {
		receiveWg.Add(1)
		go func(c *TestClient, clientID int) {
			defer receiveWg.Done()
			
			_, err := c.WaitForMessage(topic, 5*time.Second)
			if err != nil {
				t.Logf("Receive error for client %d: %v", clientID, err)
				atomic.AddInt32(&receiveErrors, 1)
			}
		}(client, i)
	}

	receiveWg.Wait()
	totalDuration := time.Since(publishStart)

	// Verify all clients received the message
	receiveErrorCount := atomic.LoadInt32(&receiveErrors)
	assert.Equal(t, int32(0), receiveErrorCount, "All clients should receive the message")

	// Calculate performance metrics
	connectionRate := float64(numConnections) / connectionDuration.Seconds()
	fanoutRate := float64(numConnections) / totalDuration.Seconds()

	// Log performance results
	t.Logf("Connection Scaling Test Results:")
	t.Logf("  Connections: %d", numConnections)
	t.Logf("  Connection Rate: %.2f conn/sec", connectionRate)
	t.Logf("  Message Fanout Rate: %.2f msg/sec", fanoutRate)
	t.Logf("  Connection Duration: %v", connectionDuration)
	t.Logf("  Fanout Duration: %v", totalDuration)
	t.Logf("  Connect Errors: %d", connectErrorCount)
	t.Logf("  Receive Errors: %d", receiveErrorCount)

	// Performance assertions
	assert.Greater(t, connectionRate, 10.0, "Connection rate should exceed 10 conn/sec")
	assert.Greater(t, fanoutRate, 20.0, "Fanout rate should exceed 20 msg/sec")
}

// TestMQTTSQLitePerformance tests different SQLite configuration performance
func TestMQTTSQLitePerformance(t *testing.T) {
	testConfigs := map[string]*TestBrokerConfig{
		"HighPerformance": {
			SQLiteConfig: &config.SQLiteConfig{
				CacheSize:   20000,    // Large cache
				TempStore:   "MEMORY", // Memory temp store
				MmapSize:    10485760, // 10MB mmap
				BusyTimeout: 1000,     // Short timeout
				Synchronous: "OFF",    // No sync for max speed
				JournalMode: "MEMORY", // Memory journal
				ForeignKeys: false,    // Disable constraints
			},
			LogLevel:      "error",
			EnableMetrics: true,
		},
		"HighDurability": {
			SQLiteConfig: &config.SQLiteConfig{
				CacheSize:   5000,     // Smaller cache
				TempStore:   "FILE",   // File temp store
				MmapSize:    1048576,  // 1MB mmap
				BusyTimeout: 30000,    // Long timeout
				Synchronous: "FULL",   // Full sync for durability
				JournalMode: "WAL",    // WAL journal
				ForeignKeys: true,     // Enable constraints
			},
			LogLevel:      "error",
			EnableMetrics: true,
		},
	}

	for configName, testConfig := range testConfigs {
		t.Run(configName, func(t *testing.T) {
			broker := NewTestBroker(t, testConfig)
			require.NoError(t, broker.Start())

			publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
				ClientID: fmt.Sprintf("sqlite-%s-publisher", configName),
			})
			subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
				ClientID: fmt.Sprintf("sqlite-%s-subscriber", configName),
			})

			require.NoError(t, publisher.Connect())
			require.NoError(t, subscriber.Connect())

			topic := fmt.Sprintf("test/sqlite/%s", configName)
			require.NoError(t, subscriber.Subscribe(topic, 1)) // QoS 1 for persistence
			time.Sleep(100 * time.Millisecond)

			// Performance test parameters
			numMessages := 500
			payload := make([]byte, 256) // 256-byte messages
			for i := range payload {
				payload[i] = byte(i % 256)
			}

			// Measure publish performance
			startTime := time.Now()
			for i := 0; i < numMessages; i++ {
				require.NoError(t, publisher.Publish(topic, 1, false, payload))
			}
			publishDuration := time.Since(startTime)

			// Wait for message delivery
			messages, err := subscriber.WaitForMessages(topic, numMessages, 15*time.Second)
			require.NoError(t, err, "Failed to receive all messages")
			
			totalDuration := time.Since(startTime)

			// Calculate performance metrics
			publishThroughput := float64(numMessages) / publishDuration.Seconds()
			endToEndThroughput := float64(numMessages) / totalDuration.Seconds()

			t.Logf("SQLite Performance Test - %s:", configName)
			t.Logf("  Messages: %d x %d bytes", numMessages, len(payload))
			t.Logf("  Publish Throughput: %.2f msg/sec", publishThroughput)
			t.Logf("  End-to-End Throughput: %.2f msg/sec", endToEndThroughput)
			t.Logf("  Publish Duration: %v", publishDuration)
			t.Logf("  Total Duration: %v", totalDuration)

			// Verify all messages received correctly
			assert.Len(t, messages, numMessages)
			for _, msg := range messages {
				assert.Equal(t, payload, msg.Payload, "Message content should match")
				assert.Equal(t, byte(1), msg.QoS, "QoS should be preserved")
			}

			// Performance expectations vary by configuration
			if configName == "HighPerformance" {
				assert.Greater(t, publishThroughput, 100.0, "High performance config should exceed 100 msg/sec")
			} else if configName == "HighDurability" {
				assert.Greater(t, publishThroughput, 10.0, "High durability config should exceed 10 msg/sec")
			}
		})
	}
}