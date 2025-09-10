package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMQTTBasicPubSub tests basic publish/subscribe functionality
func TestMQTTBasicPubSub(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start(), "Failed to start test broker")

	// Create publisher and subscriber clients
	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "test-publisher",
	})
	subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "test-subscriber",
	})

	// Connect both clients
	require.NoError(t, publisher.Connect(), "Publisher failed to connect")
	require.NoError(t, subscriber.Connect(), "Subscriber failed to connect")

	// Subscribe to topic
	topic := "test/basic"
	require.NoError(t, subscriber.Subscribe(topic, 0), "Failed to subscribe")

	// Allow subscription to propagate
	time.Sleep(100 * time.Millisecond)

	// Publish message
	message := "Hello, PromptMQ!"
	require.NoError(t, publisher.PublishString(topic, 0, false, message), "Failed to publish")

	// Wait for message delivery
	receivedMsg, err := subscriber.WaitForMessage(topic, 2*time.Second)
	require.NoError(t, err, "Failed to receive message")

	// Verify message content
	assert.Equal(t, topic, receivedMsg.Topic, "Topic mismatch")
	assert.Equal(t, []byte(message), receivedMsg.Payload, "Payload mismatch")
	assert.Equal(t, byte(0), receivedMsg.QoS, "QoS mismatch")
	assert.False(t, receivedMsg.Retained, "Should not be retained")
}

// TestMQTTQoSLevels tests all QoS levels (0, 1, 2)
func TestMQTTQoSLevels(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start(), "Failed to start test broker")

	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "qos-publisher",
	})
	subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "qos-subscriber",
	})

	require.NoError(t, publisher.Connect())
	require.NoError(t, subscriber.Connect())

	// Test each QoS level
	qosLevels := []byte{0, 1, 2}
	
	for _, qos := range qosLevels {
		t.Run(fmt.Sprintf("QoS_%d", qos), func(t *testing.T) {
			topic := fmt.Sprintf("test/qos/%d", qos)
			message := fmt.Sprintf("QoS %d message", qos)

			// Subscribe with the same QoS level
			require.NoError(t, subscriber.Subscribe(topic, qos))
			time.Sleep(100 * time.Millisecond) // Allow subscription to propagate

			// Publish with the specified QoS
			require.NoError(t, publisher.PublishString(topic, qos, false, message))

			// Wait for message delivery
			receivedMsg, err := subscriber.WaitForMessage(topic, 3*time.Second)
			require.NoError(t, err, "Failed to receive QoS %d message", qos)

			// Verify message properties
			assert.Equal(t, topic, receivedMsg.Topic)
			assert.Equal(t, []byte(message), receivedMsg.Payload)
			// Note: Received QoS might be downgraded based on subscription QoS
			assert.LessOrEqual(t, receivedMsg.QoS, qos, "Received QoS should not exceed published QoS")

			// Clean up for next test
			require.NoError(t, subscriber.Unsubscribe(topic))
			subscriber.ClearMessages()
		})
	}
}

// TestMQTTRetainedMessages tests retained message functionality
func TestMQTTRetainedMessages(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "retained-publisher",
	})
	require.NoError(t, publisher.Connect())

	topic := "test/retained"
	retainedMessage := "This message is retained"

	// Publish retained message
	require.NoError(t, publisher.PublishString(topic, 1, true, retainedMessage))

	// Allow message to be persisted
	time.Sleep(200 * time.Millisecond)

	// Create new subscriber (after retained message was published)
	subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "retained-subscriber",
	})
	require.NoError(t, subscriber.Connect())

	// Subscribe to topic - should receive retained message immediately
	require.NoError(t, subscriber.Subscribe(topic, 1))

	// Wait for retained message delivery
	receivedMsg, err := subscriber.WaitForMessage(topic, 2*time.Second)
	require.NoError(t, err, "Failed to receive retained message")

	// Verify retained message properties
	assert.Equal(t, topic, receivedMsg.Topic)
	assert.Equal(t, []byte(retainedMessage), receivedMsg.Payload)
	assert.True(t, receivedMsg.Retained, "Message should be marked as retained")

	// Test retained message deletion (empty payload with retain flag)
	require.NoError(t, publisher.Publish(topic, 1, true, []byte{}))
	time.Sleep(200 * time.Millisecond) // Allow deletion to propagate

	// Create another subscriber - should not receive the deleted retained message
	subscriber2 := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "retained-subscriber-2",
	})
	require.NoError(t, subscriber2.Connect())
	require.NoError(t, subscriber2.Subscribe(topic, 1))

	// Should not receive any messages (retained was deleted)
	_, err = subscriber2.WaitForMessage(topic, 1*time.Second)
	assert.Error(t, err, "Should not receive deleted retained message")
}

// TestMQTTWildcardSubscriptions tests wildcard subscription patterns
func TestMQTTWildcardSubscriptions(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "wildcard-publisher",
	})
	subscriber := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "wildcard-subscriber",
	})

	require.NoError(t, publisher.Connect())
	require.NoError(t, subscriber.Connect())

	t.Run("SingleLevelWildcard", func(t *testing.T) {
		// Subscribe to single-level wildcard
		wildcardTopic := "test/+/sensor"
		require.NoError(t, subscriber.Subscribe(wildcardTopic, 0))
		time.Sleep(100 * time.Millisecond)

		// Publish to topics that should match
		matchingTopics := []string{
			"test/room1/sensor",
			"test/room2/sensor",
			"test/kitchen/sensor",
		}

		for _, topic := range matchingTopics {
			message := fmt.Sprintf("Data from %s", topic)
			require.NoError(t, publisher.PublishString(topic, 0, false, message))
		}

		// Wait for all messages
		time.Sleep(500 * time.Millisecond)
		
		// Verify all matching messages were received
		for _, topic := range matchingTopics {
			messages := subscriber.GetMessages(topic)
			assert.NotEmpty(t, messages, "Should receive message for topic %s", topic)
		}

		// Publish to non-matching topic
		nonMatchingTopic := "test/room1/sensor/data" // Extra level
		require.NoError(t, publisher.PublishString(nonMatchingTopic, 0, false, "Should not match"))
		time.Sleep(200 * time.Millisecond)

		// Should not receive non-matching message
		messages := subscriber.GetMessages(nonMatchingTopic)
		assert.Empty(t, messages, "Should not receive message for non-matching topic")

		subscriber.ClearMessages()
		require.NoError(t, subscriber.Unsubscribe(wildcardTopic))
	})

	t.Run("MultiLevelWildcard", func(t *testing.T) {
		// Subscribe to multi-level wildcard
		wildcardTopic := "test/building/#"
		require.NoError(t, subscriber.Subscribe(wildcardTopic, 0))
		time.Sleep(100 * time.Millisecond)

		// Publish to topics that should match
		matchingTopics := []string{
			"test/building/floor1",
			"test/building/floor1/room1",
			"test/building/floor2/room2/sensor",
			"test/building/elevator/status/current",
		}

		for _, topic := range matchingTopics {
			message := fmt.Sprintf("Data from %s", topic)
			require.NoError(t, publisher.PublishString(topic, 0, false, message))
		}

		// Wait for all messages
		time.Sleep(500 * time.Millisecond)

		// Verify all matching messages were received
		for _, topic := range matchingTopics {
			messages := subscriber.GetMessages(topic)
			assert.NotEmpty(t, messages, "Should receive message for topic %s", topic)
		}

		// Publish to non-matching topic
		nonMatchingTopic := "test/office/room1" // Different prefix
		require.NoError(t, publisher.PublishString(nonMatchingTopic, 0, false, "Should not match"))
		time.Sleep(200 * time.Millisecond)

		// Should not receive non-matching message
		messages := subscriber.GetMessages(nonMatchingTopic)
		assert.Empty(t, messages, "Should not receive message for non-matching topic")
	})
}

// TestMQTTSessionManagement tests clean and persistent session behavior
func TestMQTTSessionManagement(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	t.Run("CleanSession", func(t *testing.T) {
		// Create client with clean session
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID:     "clean-session-client",
			CleanSession: true,
		})
		
		require.NoError(t, client.Connect())
		require.NoError(t, client.Subscribe("test/clean", 1))
		
		// Disconnect and reconnect
		client.Disconnect()
		time.Sleep(100 * time.Millisecond)
		
		// Publish message while client is disconnected
		publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "session-publisher",
		})
		require.NoError(t, publisher.Connect())
		require.NoError(t, publisher.PublishString("test/clean", 1, false, "Missed message"))
		
		// Reconnect with clean session
		require.NoError(t, client.Connect())
		require.NoError(t, client.Subscribe("test/clean", 1))
		
		// Should not receive the message sent while disconnected
		_, err := client.WaitForMessage("test/clean", 1*time.Second)
		assert.Error(t, err, "Clean session should not receive messages sent while disconnected")
	})

	t.Run("PersistentSession", func(t *testing.T) {
		clientID := "persistent-session-client"
		
		// Create client with persistent session
		client1 := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID:     clientID,
			CleanSession: false, // Persistent session
		})
		
		require.NoError(t, client1.Connect())
		require.NoError(t, client1.Subscribe("test/persistent", 1))
		client1.Disconnect()
		
		// Publish message while client is disconnected
		publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: "persistent-publisher",
		})
		require.NoError(t, publisher.Connect())
		require.NoError(t, publisher.PublishString("test/persistent", 1, false, "Queued message"))
		time.Sleep(200 * time.Millisecond) // Allow message to be queued
		
		// Reconnect with same client ID (persistent session)
		client2 := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID:     clientID,
			CleanSession: false,
		})
		require.NoError(t, client2.Connect())
		
		// Should receive the queued message
		receivedMsg, err := client2.WaitForMessage("test/persistent", 2*time.Second)
		require.NoError(t, err, "Persistent session should receive queued messages")
		assert.Equal(t, []byte("Queued message"), receivedMsg.Payload)
	})
}

// TestMQTTMultipleClients tests multiple simultaneous clients
func TestMQTTMultipleClients(t *testing.T) {
	broker := NewTestBroker(t, DefaultTestConfig())
	require.NoError(t, broker.Start())

	numClients := 5
	topic := "test/multiple"
	message := "Broadcast message"

	// Create multiple subscriber clients
	var subscribers []*TestClient
	for i := 0; i < numClients; i++ {
		client := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
			ClientID: fmt.Sprintf("multi-subscriber-%d", i),
		})
		require.NoError(t, client.Connect())
		require.NoError(t, client.Subscribe(topic, 0))
		subscribers = append(subscribers, client)
	}

	// Allow subscriptions to propagate
	time.Sleep(200 * time.Millisecond)

	// Create publisher
	publisher := NewTestClient(t, broker.MQTTAddress(), &TestClientConfig{
		ClientID: "multi-publisher",
	})
	require.NoError(t, publisher.Connect())

	// Publish message
	require.NoError(t, publisher.PublishString(topic, 0, false, message))

	// Verify all clients received the message
	for i, subscriber := range subscribers {
		receivedMsg, err := subscriber.WaitForMessage(topic, 2*time.Second)
		require.NoError(t, err, "Subscriber %d should receive message", i)
		assert.Equal(t, []byte(message), receivedMsg.Payload, "Message content mismatch for subscriber %d", i)
	}

	// Verify total message count
	totalMessages := int64(0)
	for _, subscriber := range subscribers {
		totalMessages += subscriber.GetMessageCount()
	}
	assert.Equal(t, int64(numClients), totalMessages, "Total message count should equal number of subscribers")
}