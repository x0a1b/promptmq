package integration

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMQTTMalformedPackets tests broker behavior with invalid MQTT packets
func TestMQTTMalformedPackets(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	// Test with various malformed scenarios
	t.Run("InvalidClientID", func(t *testing.T) {
		// Test with empty client ID when clean session is false
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID:     "", // Empty client ID
			CleanSession: false, // This combination should be rejected
		})

		// Connection should fail or broker should assign an ID
		err := client.Connect()
		// The behavior depends on MQTT implementation, but it shouldn't crash the broker
		if err != nil {
			t.Logf("Expected behavior: Connection rejected for empty client ID with persistent session")
		} else {
			t.Logf("Broker assigned client ID for empty client ID")
			assert.NotEmpty(t, client.ClientID(), "Broker should assign a client ID")
		}
	})

	t.Run("ExtremelyLongClientID", func(t *testing.T) {
		// Create a very long client ID (beyond typical limits)
		longClientID := strings.Repeat("x", 1000) // 1000 characters
		
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: longClientID,
		})

		// Connection might fail due to client ID length, but shouldn't crash broker
		err := client.Connect()
		if err != nil {
			t.Logf("Expected: Long client ID rejected: %v", err)
		} else {
			t.Logf("Broker accepted long client ID")
			client.Disconnect()
		}
	})

	t.Run("InvalidTopicNames", func(t *testing.T) {
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "malformed-test-client",
		})
		require.NoError(t, client.Connect())

		// Test various invalid topic names
		invalidTopics := []string{
			"",                    // Empty topic
			strings.Repeat("x", 10000), // Extremely long topic
			"topic\x00with\x00nulls",   // Topic with null bytes
			"topic/with/+/wildcard",    // Wildcard in publish (should fail)
			"topic/with/#/wildcard",    // Multi-level wildcard in publish
		}

		for _, topic := range invalidTopics {
			err := client.PublishString(topic, 0, false, "test message")
			// Invalid topics should either be rejected or handled gracefully
			if err != nil {
				t.Logf("Invalid topic '%s' rejected: %v", 
					strings.ReplaceAll(topic, "\x00", "\\x00"), err)
			} else {
				t.Logf("Warning: Invalid topic '%s' was accepted", 
					strings.ReplaceAll(topic, "\x00", "\\x00"))
			}
		}
	})
}

// TestMQTTResourceExhaustion tests broker behavior under resource pressure
func TestMQTTResourceExhaustion(t *testing.T) {
	// Use a restrictive configuration to test limits
	config := DefaultTestConfig()
	config.SQLiteConfig.CacheSize = 100 // Very small cache
	
	broker := NewTestBroker(t, config)
	require.NoError(t, broker.Start())

	t.Run("ConnectionLimit", func(t *testing.T) {
		maxConnections := 20 // Reasonable limit for testing
		var clients []*TestClient
		var connectionErrors int32

		// Try to create more connections than reasonable
		for i := 0; i < maxConnections*2; i++ {
			client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
				ClientID: fmt.Sprintf("exhaust-client-%d", i),
			})

			err := client.Connect()
			if err != nil {
				atomic.AddInt32(&connectionErrors, 1)
				t.Logf("Connection %d failed (expected): %v", i, err)
			} else {
				clients = append(clients, client)
			}
		}

		t.Logf("Successfully connected: %d clients", len(clients))
		t.Logf("Connection errors: %d", atomic.LoadInt32(&connectionErrors))
		
		// Broker should either accept connections or reject them gracefully
		assert.NotZero(t, len(clients), "Some connections should succeed")
		
		// Cleanup
		for _, client := range clients {
			client.Disconnect()
		}
	})

	t.Run("MemoryExhaustion", func(t *testing.T) {
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "memory-exhaust-client",
		})
		require.NoError(t, client.Connect())

		// Try to exhaust memory with large messages
		largePayload := make([]byte, 1024*1024) // 1MB payload
		for i := range largePayload {
			largePayload[i] = byte(i % 256)
		}

		var publishErrors int32
		numLargeMessages := 10

		for i := 0; i < numLargeMessages; i++ {
			topic := fmt.Sprintf("test/memory/large/%d", i)
			err := client.Publish(topic, 0, false, largePayload)
			if err != nil {
				atomic.AddInt32(&publishErrors, 1)
				t.Logf("Large message %d failed: %v", i, err)
			}
		}

		t.Logf("Large message publish errors: %d", atomic.LoadInt32(&publishErrors))
		
		// Broker should handle large messages or reject them gracefully
		if atomic.LoadInt32(&publishErrors) == 0 {
			t.Logf("Broker successfully handled all large messages")
		} else {
			t.Logf("Broker rejected some large messages (acceptable behavior)")
		}
	})

	t.Run("MessageFlood", func(t *testing.T) {
		publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "flood-publisher",
		})
		subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "flood-subscriber",
		})

		require.NoError(t, publisher.Connect())
		require.NoError(t, subscriber.Connect())
		require.NoError(t, subscriber.Subscribe("flood/+", 0))

		time.Sleep(100 * time.Millisecond) // Allow subscription

		// Flood with messages as fast as possible
		numFloodMessages := 1000
		floodPayload := []byte("flood message")
		
		startTime := time.Now()
		var floodErrors int32

		for i := 0; i < numFloodMessages; i++ {
			topic := fmt.Sprintf("flood/%d", i%10) // 10 different topics
			err := publisher.Publish(topic, 0, false, floodPayload)
			if err != nil {
				atomic.AddInt32(&floodErrors, 1)
			}
		}

		floodDuration := time.Since(startTime)
		
		// Wait for some message delivery
		time.Sleep(2 * time.Second)
		
		receivedCount := subscriber.GetMessageCount()
		floodErrorCount := atomic.LoadInt32(&floodErrors)
		
		t.Logf("Flood test results:")
		t.Logf("  Sent: %d messages in %v", numFloodMessages, floodDuration)
		t.Logf("  Errors: %d", floodErrorCount)
		t.Logf("  Received: %d", receivedCount)
		t.Logf("  Throughput: %.2f msg/sec", float64(numFloodMessages)/floodDuration.Seconds())
		
		// Broker should handle the flood without crashing
		assert.Greater(t, receivedCount, int64(0), "Should receive some messages")
	})
}

// TestMQTTEdgeCases tests various edge case scenarios
func TestMQTTEdgeCases(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	t.Run("EmptyPayloads", func(t *testing.T) {
		publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "empty-publisher",
		})
		subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "empty-subscriber",
		})

		require.NoError(t, publisher.Connect())
		require.NoError(t, subscriber.Connect())
		require.NoError(t, subscriber.Subscribe("test/empty", 0))

		time.Sleep(100 * time.Millisecond)

		// Test empty payload
		err := publisher.Publish("test/empty", 0, false, []byte{})
		require.NoError(t, err, "Empty payload should be allowed")

		// Test nil payload (using raw publish)
		err = publisher.Publish("test/empty", 0, false, nil)
		require.NoError(t, err, "Nil payload should be allowed")

		// Wait for message delivery
		time.Sleep(500 * time.Millisecond)
		
		messages := subscriber.GetMessages("test/empty")
		assert.GreaterOrEqual(t, len(messages), 1, "Should receive empty payload messages")
		
		for _, msg := range messages {
			assert.True(t, len(msg.Payload) == 0, "Payload should be empty")
		}
	})

	t.Run("SpecialCharactersInPayload", func(t *testing.T) {
		publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "special-publisher",
		})
		subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "special-subscriber",
		})

		require.NoError(t, publisher.Connect())
		require.NoError(t, subscriber.Connect())
		require.NoError(t, subscriber.Subscribe("test/special", 0))

		time.Sleep(100 * time.Millisecond)

		// Test various special characters and binary data
		specialPayloads := [][]byte{
			[]byte{0x00, 0x01, 0x02, 0xFF}, // Binary data
			[]byte("Hello\x00World"),        // Null bytes
			[]byte("Unicode: 🚀🔥💯"),        // Unicode characters
			[]byte("\r\n\t"),                // Control characters
			bytes.Repeat([]byte("x"), 1000), // Long payload
		}

		for i, payload := range specialPayloads {
			topic := fmt.Sprintf("test/special/%d", i)
			err := publisher.Publish(topic, 0, false, payload)
			require.NoError(t, err, "Special payload %d should be accepted", i)
		}

		// Wait for message delivery
		time.Sleep(1 * time.Second)
		
		totalReceived := subscriber.GetMessageCount()
		assert.Equal(t, int64(len(specialPayloads)), totalReceived, 
			"Should receive all special payload messages")
	})

	t.Run("RapidConnectionCycles", func(t *testing.T) {
		// Test rapid connect/disconnect cycles
		clientID := "rapid-cycle-client"
		numCycles := 20
		
		var cycleErrors int32
		
		for i := 0; i < numCycles; i++ {
			client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
				ClientID: fmt.Sprintf("%s-%d", clientID, i),
				ConnectTimeout: 2 * time.Second,
			})
			
			err := client.Connect()
			if err != nil {
				atomic.AddInt32(&cycleErrors, 1)
				continue
			}
			
			// Immediately disconnect
			client.Disconnect()
			
			// Small delay to avoid overwhelming
			time.Sleep(10 * time.Millisecond)
		}
		
		cycleErrorCount := atomic.LoadInt32(&cycleErrors)
		t.Logf("Rapid connection cycle errors: %d/%d", cycleErrorCount, numCycles)
		
		// Most cycles should succeed
		assert.Less(t, cycleErrorCount, int32(numCycles/2), 
			"Most connection cycles should succeed")
	})

	t.Run("InvalidQoSLevels", func(t *testing.T) {
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "invalid-qos-client",
		})
		require.NoError(t, client.Connect())

		// Test invalid QoS levels
		invalidQoSLevels := []byte{3, 4, 255}
		
		for _, qos := range invalidQoSLevels {
			err := client.PublishString("test/invalid-qos", qos, false, "test message")
			// Invalid QoS should be rejected or normalized
			if err != nil {
				t.Logf("Invalid QoS %d rejected: %v", qos, err)
			} else {
				t.Logf("Warning: Invalid QoS %d was accepted", qos)
			}
		}
	})
}

// TestMQTTErrorRecovery tests graceful degradation under various error conditions
func TestMQTTErrorRecovery(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	t.Run("NetworkInterruption", func(t *testing.T) {
		// Test behavior when client connections are abruptly closed
		var clients []*TestClient
		numClients := 10

		// Create multiple connected clients
		for i := 0; i < numClients; i++ {
			client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
				ClientID: fmt.Sprintf("network-client-%d", i),
			})
			require.NoError(t, client.Connect())
			clients = append(clients, client)
		}

		// Abruptly disconnect half the clients (simulating network issues)
		for i := 0; i < numClients/2; i++ {
			clients[i].Disconnect()
		}

		// Allow broker to detect disconnections
		time.Sleep(500 * time.Millisecond)

		// Remaining clients should still work
		workingClient := clients[numClients-1]
		assert.True(t, workingClient.IsConnected(), "Remaining client should still be connected")

		err := workingClient.PublishString("test/recovery", 0, false, "recovery test")
		assert.NoError(t, err, "Remaining client should still be able to publish")
	})

	t.Run("ConcurrentOperations", func(t *testing.T) {
		// Test concurrent operations on same client
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "concurrent-ops-client",
		})
		require.NoError(t, client.Connect())

		var wg sync.WaitGroup
		var operationErrors int32
		numOperations := 50

		// Concurrent publish operations
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func(opID int) {
				defer wg.Done()
				
				topic := fmt.Sprintf("test/concurrent/%d", opID)
				message := fmt.Sprintf("concurrent message %d", opID)
				
				err := client.PublishString(topic, 0, false, message)
				if err != nil {
					atomic.AddInt32(&operationErrors, 1)
					t.Logf("Concurrent operation %d failed: %v", opID, err)
				}
			}(i)
		}

		wg.Wait()
		
		opErrors := atomic.LoadInt32(&operationErrors)
		t.Logf("Concurrent operation errors: %d/%d", opErrors, numOperations)
		
		// Most operations should succeed
		assert.Less(t, opErrors, int32(numOperations/4), 
			"Most concurrent operations should succeed")
	})

	t.Run("SubscriptionStorm", func(t *testing.T) {
		// Test rapid subscribe/unsubscribe operations
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "subscription-storm-client",
		})
		require.NoError(t, client.Connect())

		numTopics := 100
		var subscriptionErrors int32

		// Rapid subscribe to many topics
		for i := 0; i < numTopics; i++ {
			topic := fmt.Sprintf("storm/topic/%d", i)
			err := client.Subscribe(topic, 0)
			if err != nil {
				atomic.AddInt32(&subscriptionErrors, 1)
			}
		}

		// Rapid unsubscribe from all topics
		for i := 0; i < numTopics; i++ {
			topic := fmt.Sprintf("storm/topic/%d", i)
			err := client.Unsubscribe(topic)
			if err != nil {
				atomic.AddInt32(&subscriptionErrors, 1)
			}
		}

		subErrors := atomic.LoadInt32(&subscriptionErrors)
		t.Logf("Subscription storm errors: %d/%d", subErrors, numTopics*2)
		
		// Most subscription operations should succeed
		assert.Less(t, subErrors, int32(numTopics/2), 
			"Most subscription operations should succeed")
	})
}

// TestMQTTFuzzTesting performs fuzz-like testing with random data
func TestMQTTFuzzTesting(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "fuzz-test-client",
	})
	require.NoError(t, client.Connect())

	rand.Seed(time.Now().UnixNano())

	t.Run("RandomPayloads", func(t *testing.T) {
		numTests := 100
		var fuzzErrors int32

		for i := 0; i < numTests; i++ {
			// Generate random payload
			payloadSize := rand.Intn(1000) + 1 // 1 to 1000 bytes
			payload := make([]byte, payloadSize)
			rand.Read(payload)

			topic := fmt.Sprintf("fuzz/random/%d", i)
			err := client.Publish(topic, 0, false, payload)
			if err != nil {
				atomic.AddInt32(&fuzzErrors, 1)
				t.Logf("Fuzz test %d failed: %v", i, err)
			}
		}

		fuzzErrorCount := atomic.LoadInt32(&fuzzErrors)
		t.Logf("Random payload fuzz errors: %d/%d", fuzzErrorCount, numTests)
		
		// Most random payloads should be accepted
		assert.Less(t, fuzzErrorCount, int32(numTests/10), 
			"Most random payloads should be accepted")
	})

	t.Run("RandomTopics", func(t *testing.T) {
		numTests := 50
		var topicErrors int32

		for i := 0; i < numTests; i++ {
			// Generate random topic name
			topicLength := rand.Intn(100) + 1
			topicBytes := make([]byte, topicLength)
			
			// Use printable characters mostly, with some random bytes
			for j := range topicBytes {
				if rand.Float32() < 0.8 {
					topicBytes[j] = byte(32 + rand.Intn(95)) // Printable ASCII
				} else {
					topicBytes[j] = byte(rand.Intn(256)) // Any byte
				}
			}
			
			topic := string(topicBytes)
			err := client.PublishString(topic, 0, false, "fuzz message")
			if err != nil {
				atomic.AddInt32(&topicErrors, 1)
				t.Logf("Random topic test %d failed: %v", i, err)
			}
		}

		topicErrorCount := atomic.LoadInt32(&topicErrors)
		t.Logf("Random topic fuzz errors: %d/%d", topicErrorCount, numTests)
		
		// Some random topics might be rejected, which is acceptable
		t.Logf("Random topic testing completed")
	})
}