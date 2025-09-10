package integration

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/require"
)

// TestClient provides a reliable MQTT client for integration testing
type TestClient struct {
	client        mqtt.Client
	clientID      string
	t             *testing.T
	messages      sync.Map  // topic -> []ReceivedMessage
	messageCount  int64     // atomic counter
	connected     int32     // atomic boolean
	subscriptions sync.Map  // topic -> subscription info
}

// ReceivedMessage represents a message received by the test client
type ReceivedMessage struct {
	Topic     string
	Payload   []byte
	QoS       byte
	Retained  bool
	MessageID uint16
	Timestamp time.Time
}

// SubscriptionInfo tracks subscription details
type SubscriptionInfo struct {
	Topic         string
	QoS           byte
	MessageCount  int64 // atomic counter
	LastMessage   *ReceivedMessage
	ExpectedCount int   // for test validation
}

// TestClientConfig holds configuration for test clients
type TestClientConfig struct {
	ClientID      string
	CleanSession  bool
	KeepAlive     time.Duration
	ConnectTimeout time.Duration
	Username      string
	Password      string
}

// NewTestClient creates a new MQTT test client
func NewTestClient(t *testing.T, brokerAddr string, config *TestClientConfig) *TestClient {
	if config == nil {
		config = &TestClientConfig{
			ClientID:       fmt.Sprintf("test-client-%d", time.Now().UnixNano()),
			CleanSession:   true,
			KeepAlive:      30 * time.Second,
			ConnectTimeout: 5 * time.Second,
		}
	}

	// Ensure unique client ID
	if config.ClientID == "" {
		config.ClientID = fmt.Sprintf("test-client-%d", time.Now().UnixNano())
	}

	tc := &TestClient{
		clientID: config.ClientID,
		t:        t,
	}

	// Configure MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerAddr)
	opts.SetClientID(config.ClientID)
	
	t.Logf("Creating MQTT client for %s connecting to %s", config.ClientID, brokerAddr)
	opts.SetCleanSession(config.CleanSession)
	opts.SetKeepAlive(config.KeepAlive)
	opts.SetConnectTimeout(config.ConnectTimeout)
	opts.SetAutoReconnect(false) // Explicit control in tests
	opts.SetProtocolVersion(4)   // Use MQTT v3.1.1 (protocol version 4)
	
	if config.Username != "" {
		opts.SetUsername(config.Username)
	}
	if config.Password != "" {
		opts.SetPassword(config.Password)
	}

	// Set connection handlers
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		atomic.StoreInt32(&tc.connected, 1)
		t.Logf("Test client %s connected", config.ClientID)
	})

	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		atomic.StoreInt32(&tc.connected, 0)
		t.Logf("Test client %s connection lost: %v", config.ClientID, err)
	})

	// Set default message handler
	opts.SetDefaultPublishHandler(func(c mqtt.Client, msg mqtt.Message) {
		tc.handleMessage(msg)
	})

	tc.client = mqtt.NewClient(opts)

	// Ensure cleanup
	t.Cleanup(func() {
		tc.Disconnect()
	})

	return tc
}

// Connect connects the client to the broker
func (tc *TestClient) Connect() error {
	tc.t.Logf("Attempting to connect client %s", tc.clientID)
	token := tc.client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("connection timeout for client %s", tc.clientID)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("connection failed for client %s: %w", tc.clientID, err)
	}
	atomic.StoreInt32(&tc.connected, 1)
	tc.t.Logf("Client %s connected successfully", tc.clientID)
	return nil
}

// Disconnect disconnects the client from the broker
func (tc *TestClient) Disconnect() {
	if tc.client.IsConnected() {
		tc.client.Disconnect(250) // 250ms quiesce time
		atomic.StoreInt32(&tc.connected, 0)
		tc.t.Logf("Test client %s disconnected", tc.clientID)
	}
}

// IsConnected returns true if the client is connected
func (tc *TestClient) IsConnected() bool {
	return atomic.LoadInt32(&tc.connected) == 1 && tc.client.IsConnected()
}

// Subscribe subscribes to a topic with the given QoS
func (tc *TestClient) Subscribe(topic string, qos byte) error {
	if !tc.IsConnected() {
		return fmt.Errorf("client %s not connected", tc.clientID)
	}

	token := tc.client.Subscribe(topic, qos, func(c mqtt.Client, msg mqtt.Message) {
		tc.handleMessage(msg)
	})

	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("subscribe timeout for topic %s", topic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("subscribe failed for topic %s: %w", topic, err)
	}

	// Track subscription
	tc.subscriptions.Store(topic, &SubscriptionInfo{
		Topic: topic,
		QoS:   qos,
	})

	tc.t.Logf("Client %s subscribed to topic %s with QoS %d", tc.clientID, topic, qos)
	return nil
}

// Unsubscribe unsubscribes from a topic
func (tc *TestClient) Unsubscribe(topic string) error {
	if !tc.IsConnected() {
		return fmt.Errorf("client %s not connected", tc.clientID)
	}

	token := tc.client.Unsubscribe(topic)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("unsubscribe timeout for topic %s", topic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("unsubscribe failed for topic %s: %w", topic, err)
	}

	tc.subscriptions.Delete(topic)
	tc.t.Logf("Client %s unsubscribed from topic %s", tc.clientID, topic)
	return nil
}

// Publish publishes a message to a topic
func (tc *TestClient) Publish(topic string, qos byte, retained bool, payload []byte) error {
	if !tc.IsConnected() {
		return fmt.Errorf("client %s not connected", tc.clientID)
	}

	token := tc.client.Publish(topic, qos, retained, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("publish timeout for topic %s", topic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("publish failed for topic %s: %w", topic, err)
	}

	tc.t.Logf("Client %s published to topic %s (QoS %d, retained %v, %d bytes)", 
		tc.clientID, topic, qos, retained, len(payload))
	return nil
}

// PublishString publishes a string message to a topic
func (tc *TestClient) PublishString(topic string, qos byte, retained bool, message string) error {
	return tc.Publish(topic, qos, retained, []byte(message))
}

// handleMessage processes incoming messages
func (tc *TestClient) handleMessage(msg mqtt.Message) {
	received := &ReceivedMessage{
		Topic:     msg.Topic(),
		Payload:   msg.Payload(),
		QoS:       msg.Qos(),
		Retained:  msg.Retained(),
		MessageID: msg.MessageID(),
		Timestamp: time.Now(),
	}

	// Store message
	var messages []*ReceivedMessage
	if existing, ok := tc.messages.Load(msg.Topic()); ok {
		messages = existing.([]*ReceivedMessage)
	}
	messages = append(messages, received)
	tc.messages.Store(msg.Topic(), messages)

	// Update counters
	atomic.AddInt64(&tc.messageCount, 1)
	
	// Update subscription info
	if subInfo, ok := tc.subscriptions.Load(msg.Topic()); ok {
		sub := subInfo.(*SubscriptionInfo)
		atomic.AddInt64(&sub.MessageCount, 1)
		sub.LastMessage = received
	}

	tc.t.Logf("Client %s received message on topic %s: %s", 
		tc.clientID, msg.Topic(), string(msg.Payload()))
}

// GetMessages returns all messages received for a topic
func (tc *TestClient) GetMessages(topic string) []*ReceivedMessage {
	if messages, ok := tc.messages.Load(topic); ok {
		return messages.([]*ReceivedMessage)
	}
	return nil
}

// GetAllMessages returns all received messages across all topics
func (tc *TestClient) GetAllMessages() map[string][]*ReceivedMessage {
	result := make(map[string][]*ReceivedMessage)
	tc.messages.Range(func(key, value interface{}) bool {
		topic := key.(string)
		messages := value.([]*ReceivedMessage)
		result[topic] = messages
		return true
	})
	return result
}

// GetMessageCount returns the total number of messages received
func (tc *TestClient) GetMessageCount() int64 {
	return atomic.LoadInt64(&tc.messageCount)
}

// GetMessageCountForTopic returns the number of messages received for a specific topic
func (tc *TestClient) GetMessageCountForTopic(topic string) int64 {
	if subInfo, ok := tc.subscriptions.Load(topic); ok {
		sub := subInfo.(*SubscriptionInfo)
		return atomic.LoadInt64(&sub.MessageCount)
	}
	
	// Fallback: count messages manually
	if messages, ok := tc.messages.Load(topic); ok {
		return int64(len(messages.([]*ReceivedMessage)))
	}
	return 0
}

// WaitForMessage waits for at least one message on the specified topic
func (tc *TestClient) WaitForMessage(topic string, timeout time.Duration) (*ReceivedMessage, error) {
	deadline := time.Now().Add(timeout)
	
	for time.Now().Before(deadline) {
		if messages, ok := tc.messages.Load(topic); ok {
			msgs := messages.([]*ReceivedMessage)
			if len(msgs) > 0 {
				return msgs[len(msgs)-1], nil // Return latest message
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	
	return nil, fmt.Errorf("no message received on topic %s within timeout", topic)
}

// WaitForMessages waits for a specific number of messages on a topic
func (tc *TestClient) WaitForMessages(topic string, count int, timeout time.Duration) ([]*ReceivedMessage, error) {
	deadline := time.Now().Add(timeout)
	
	for time.Now().Before(deadline) {
		if messages, ok := tc.messages.Load(topic); ok {
			msgs := messages.([]*ReceivedMessage)
			if len(msgs) >= count {
				return msgs, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	
	currentCount := tc.GetMessageCountForTopic(topic)
	return nil, fmt.Errorf("expected %d messages on topic %s, got %d within timeout", 
		count, topic, currentCount)
}

// ClearMessages clears all received messages
func (tc *TestClient) ClearMessages() {
	tc.messages = sync.Map{}
	atomic.StoreInt64(&tc.messageCount, 0)
	
	// Reset subscription counters
	tc.subscriptions.Range(func(key, value interface{}) bool {
		sub := value.(*SubscriptionInfo)
		atomic.StoreInt64(&sub.MessageCount, 0)
		sub.LastMessage = nil
		return true
	})
}

// ClientID returns the client ID
func (tc *TestClient) ClientID() string {
	return tc.clientID
}

// AssertMessageReceived asserts that a message was received on the topic
func (tc *TestClient) AssertMessageReceived(topic string, expectedPayload []byte) {
	messages := tc.GetMessages(topic)
	require.NotEmpty(tc.t, messages, "No messages received on topic %s", topic)
	
	// Check if any message matches the expected payload
	for _, msg := range messages {
		if string(msg.Payload) == string(expectedPayload) {
			return // Found matching message
		}
	}
	
	require.Failf(tc.t, "Expected message not found", 
		"Expected payload %q not found in messages on topic %s", 
		string(expectedPayload), topic)
}

// AssertMessageCount asserts the number of messages received on a topic
func (tc *TestClient) AssertMessageCount(topic string, expectedCount int) {
	actualCount := tc.GetMessageCountForTopic(topic)
	require.Equal(tc.t, int64(expectedCount), actualCount, 
		"Expected %d messages on topic %s, got %d", expectedCount, topic, actualCount)
}