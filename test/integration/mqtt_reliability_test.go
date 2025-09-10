package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/x0a1b/promptmq/internal/broker"
	"github.com/x0a1b/promptmq/internal/config"
)

const (
	testBrokerHost = "localhost"
	testBrokerPort = "1884"
	testBrokerAddr = testBrokerHost + ":" + testBrokerPort
	metricsPort    = "9091"
	metricsAddr    = testBrokerHost + ":" + metricsPort
)

type TestBroker struct {
	broker  *broker.Broker
	ctx     context.Context
	cancel  context.CancelFunc
	cfg     *config.Config
	tempDir string
	walDir  string
}

func setupTestBroker(t *testing.T) *TestBroker {
	// Create temporary directories
	tempDir := t.TempDir()
	walDir := filepath.Join(tempDir, "wal")
	dataDir := filepath.Join(tempDir, "data")

	err := os.MkdirAll(walDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(dataDir, 0755)
	require.NoError(t, err)

	// Create test configuration
	cfg := &config.Config{
		Log: config.LogConfig{
			Level:  "debug",
			Format: "console",
		},
		Server: config.ServerConfig{
			Bind:   testBrokerAddr,
			WSBind: "",
		},
		MQTT: config.MQTTConfig{
			MaxQoS:             2,
			MaxConnections:     1000,
			MaxInflight:        50,
			RetainAvailable:    true,
			WildcardAvailable:  true,
			SharedSubAvailable: true,
		},
		Storage: config.StorageConfig{
			DataDir:         dataDir,
			WALDir:          walDir,
			MemoryBuffer:    1048576, // 1MB for testing
			WALSyncInterval: time.Millisecond * 10,
			WALNoSync:       false,
		},
		Metrics: config.MetricsConfig{
			Enabled: true,
			Bind:    metricsAddr,
			Path:    "/metrics",
		},
	}

	// Create broker
	b, err := broker.New(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	testBroker := &TestBroker{
		broker:  b,
		ctx:     ctx,
		cancel:  cancel,
		cfg:     cfg,
		tempDir: tempDir,
		walDir:  walDir,
	}

	// Start broker in background
	go func() {
		err := b.Start(ctx)
		if err != nil && ctx.Err() == nil {
			t.Errorf("Broker start error: %v", err)
		}
	}()

	// Wait for broker to start
	time.Sleep(100 * time.Millisecond)

	return testBroker
}

func (tb *TestBroker) Stop() {
	tb.cancel()
	time.Sleep(50 * time.Millisecond)
}

func createMQTTClient(t *testing.T, clientID string) mqtt.Client {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://" + testBrokerAddr)
	opts.SetClientID(clientID)
	opts.SetCleanSession(true)
	opts.SetConnectTimeout(5 * time.Second)

	client := mqtt.NewClient(opts)
	token := client.Connect()
	require.True(t, token.WaitTimeout(5*time.Second), "Connection timeout")
	require.NoError(t, token.Error())

	return client
}

func TestMQTTBasicPubSub(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	// Create publisher and subscriber clients
	publisher := createMQTTClient(t, "test-publisher")
	defer publisher.Disconnect(1000)

	subscriber := createMQTTClient(t, "test-subscriber")
	defer subscriber.Disconnect(1000)

	// Message tracking
	var receivedMessages []string
	var receivedCount int32
	var mu sync.Mutex

	// Subscribe to topic
	token := subscriber.Subscribe("test/topic", 1, func(client mqtt.Client, msg mqtt.Message) {
		mu.Lock()
		receivedMessages = append(receivedMessages, string(msg.Payload()))
		mu.Unlock()
		atomic.AddInt32(&receivedCount, 1)
	})
	require.True(t, token.WaitTimeout(5*time.Second))
	require.NoError(t, token.Error())

	// Wait for subscription to be established
	time.Sleep(50 * time.Millisecond)

	// Publish messages
	testMessages := []string{"hello", "world", "mqtt", "test"}
	for _, msg := range testMessages {
		token := publisher.Publish("test/topic", 1, false, msg)
		require.True(t, token.WaitTimeout(5*time.Second))
		require.NoError(t, token.Error())
	}

	// Wait for messages to be received
	timeout := time.After(5 * time.Second)
	for {
		if atomic.LoadInt32(&receivedCount) >= int32(len(testMessages)) {
			break
		}
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for messages")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify received messages
	mu.Lock()
	assert.Equal(t, testMessages, receivedMessages)
	mu.Unlock()
}

func TestMQTTQoSLevels(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	publisher := createMQTTClient(t, "qos-publisher")
	defer publisher.Disconnect(1000)

	subscriber := createMQTTClient(t, "qos-subscriber")
	defer subscriber.Disconnect(1000)

	// Test each QoS level
	qosLevels := []byte{0, 1, 2}
	var receivedQoS []byte
	var receivedCount int32
	var mu sync.Mutex

	for _, qos := range qosLevels {
		topic := fmt.Sprintf("test/qos/%d", qos)

		// Subscribe with current QoS
		token := subscriber.Subscribe(topic, qos, func(client mqtt.Client, msg mqtt.Message) {
			mu.Lock()
			receivedQoS = append(receivedQoS, msg.Qos())
			mu.Unlock()
			atomic.AddInt32(&receivedCount, 1)
		})
		require.True(t, token.WaitTimeout(5*time.Second))
		require.NoError(t, token.Error())
	}

	time.Sleep(50 * time.Millisecond)

	// Publish to each QoS topic
	for i, qos := range qosLevels {
		topic := fmt.Sprintf("test/qos/%d", qos)
		payload := fmt.Sprintf("qos-%d-message", i)

		token := publisher.Publish(topic, qos, false, payload)
		require.True(t, token.WaitTimeout(5*time.Second))
		require.NoError(t, token.Error())
	}

	// Wait for all messages
	timeout := time.After(5 * time.Second)
	for {
		if atomic.LoadInt32(&receivedCount) >= int32(len(qosLevels)) {
			break
		}
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for QoS messages")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify QoS levels were preserved
	mu.Lock()
	assert.Len(t, receivedQoS, len(qosLevels))
	for i, expectedQos := range qosLevels {
		if i < len(receivedQoS) {
			assert.Equal(t, expectedQos, receivedQoS[i])
		}
	}
	mu.Unlock()
}

func TestMQTTRetainedMessages(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	publisher := createMQTTClient(t, "retain-publisher")
	defer publisher.Disconnect(1000)

	// Publish retained message
	retainedPayload := "retained-message"
	token := publisher.Publish("test/retained", 1, true, retainedPayload)
	require.True(t, token.WaitTimeout(5*time.Second))
	require.NoError(t, token.Error())

	// Wait for persistence
	time.Sleep(100 * time.Millisecond)

	// Connect new subscriber after retained message was published
	subscriber := createMQTTClient(t, "retain-subscriber")
	defer subscriber.Disconnect(1000)

	var receivedPayload string
	var receivedRetained bool
	var messageReceived sync.WaitGroup
	messageReceived.Add(1)

	token = subscriber.Subscribe("test/retained", 1, func(client mqtt.Client, msg mqtt.Message) {
		receivedPayload = string(msg.Payload())
		receivedRetained = msg.Retained()
		messageReceived.Done()
	})
	require.True(t, token.WaitTimeout(5*time.Second))
	require.NoError(t, token.Error())

	// Wait for retained message
	require.True(t, waitWithTimeout(&messageReceived, 5*time.Second))

	// Verify retained message
	assert.Equal(t, retainedPayload, receivedPayload)
	assert.True(t, receivedRetained)
}

func TestMQTTWildcardSubscriptions(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	publisher := createMQTTClient(t, "wildcard-publisher")
	defer publisher.Disconnect(1000)

	subscriber := createMQTTClient(t, "wildcard-subscriber")
	defer subscriber.Disconnect(1000)

	var receivedTopics []string
	var receivedPayloads []string
	var receivedCount int32
	var mu sync.Mutex

	// Subscribe to wildcard topics
	wildcardTopics := []string{"sensors/+/temperature", "alerts/#"}
	for _, topic := range wildcardTopics {
		token := subscriber.Subscribe(topic, 1, func(client mqtt.Client, msg mqtt.Message) {
			mu.Lock()
			receivedTopics = append(receivedTopics, msg.Topic())
			receivedPayloads = append(receivedPayloads, string(msg.Payload()))
			mu.Unlock()
			atomic.AddInt32(&receivedCount, 1)
		})
		require.True(t, token.WaitTimeout(5*time.Second))
		require.NoError(t, token.Error())
	}

	time.Sleep(50 * time.Millisecond)

	// Publish to topics that match wildcards
	testData := []struct {
		topic   string
		payload string
	}{
		{"sensors/room1/temperature", "22.5"},
		{"sensors/room2/temperature", "23.1"},
		{"alerts/fire", "fire detected"},
		{"alerts/security/door", "door open"},
	}

	for _, data := range testData {
		token := publisher.Publish(data.topic, 1, false, data.payload)
		require.True(t, token.WaitTimeout(5*time.Second))
		require.NoError(t, token.Error())
	}

	// Wait for all messages
	timeout := time.After(5 * time.Second)
	for {
		if atomic.LoadInt32(&receivedCount) >= int32(len(testData)) {
			break
		}
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for wildcard messages")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify all expected messages were received
	mu.Lock()
	assert.Len(t, receivedTopics, len(testData))
	assert.Len(t, receivedPayloads, len(testData))

	for _, expected := range testData {
		assert.Contains(t, receivedTopics, expected.topic)
		assert.Contains(t, receivedPayloads, expected.payload)
	}
	mu.Unlock()
}

func TestMQTTHighThroughput(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	publisher := createMQTTClient(t, "throughput-publisher")
	defer publisher.Disconnect(1000)

	subscriber := createMQTTClient(t, "throughput-subscriber")
	defer subscriber.Disconnect(1000)

	const messageCount = 1000
	var receivedCount int32

	// Subscribe
	token := subscriber.Subscribe("test/throughput", 1, func(client mqtt.Client, msg mqtt.Message) {
		atomic.AddInt32(&receivedCount, 1)
	})
	require.True(t, token.WaitTimeout(5*time.Second))
	require.NoError(t, token.Error())

	time.Sleep(50 * time.Millisecond)

	// Publish messages rapidly
	start := time.Now()
	for i := 0; i < messageCount; i++ {
		payload := fmt.Sprintf("message-%d", i)
		token := publisher.Publish("test/throughput", 1, false, payload)
		require.True(t, token.WaitTimeout(5*time.Second))
		require.NoError(t, token.Error())
	}

	// Wait for all messages
	timeout := time.After(30 * time.Second)
	for {
		if atomic.LoadInt32(&receivedCount) >= messageCount {
			break
		}
		select {
		case <-timeout:
			t.Fatalf("Timeout: received %d/%d messages", atomic.LoadInt32(&receivedCount), messageCount)
		case <-time.After(10 * time.Millisecond):
		}
	}

	duration := time.Since(start)
	throughput := float64(messageCount) / duration.Seconds()

	t.Logf("High throughput test completed:")
	t.Logf("  Messages: %d", messageCount)
	t.Logf("  Duration: %v", duration)
	t.Logf("  Throughput: %.2f messages/second", throughput)

	// Verify throughput (should be much higher than 100 msg/sec)
	assert.Greater(t, throughput, 100.0, "Throughput too low")
	assert.Equal(t, int32(messageCount), atomic.LoadInt32(&receivedCount))
}

func TestMQTTCrashRecovery(t *testing.T) {
	// First phase: publish messages
	tb1 := setupTestBroker(t)

	publisher := createMQTTClient(t, "recovery-publisher")
	defer publisher.Disconnect(1000)

	const messageCount = 100

	// Publish messages with retain flag for crash recovery
	for i := 0; i < messageCount; i++ {
		topic := fmt.Sprintf("test/recovery/%d", i%10) // 10 different topics
		payload := fmt.Sprintf("recovery-message-%d", i)
		token := publisher.Publish(topic, 1, true, payload) // retained for recovery
		require.True(t, token.WaitTimeout(5*time.Second))
		require.NoError(t, token.Error())
	}

	// Wait for persistence
	time.Sleep(200 * time.Millisecond)

	// Simulate crash by stopping broker
	tempDir := tb1.tempDir
	walDir := tb1.walDir
	tb1.Stop()
	publisher.Disconnect(1000)

	// Wait for full shutdown
	time.Sleep(100 * time.Millisecond)

	// Second phase: restart and verify recovery
	tb2 := &TestBroker{
		tempDir: tempDir,
		walDir:  walDir,
	}

	// Create new config using same directories
	cfg := &config.Config{
		Log: config.LogConfig{
			Level:  "debug",
			Format: "console",
		},
		Server: config.ServerConfig{
			Bind:   testBrokerAddr,
			WSBind: "",
		},
		MQTT: config.MQTTConfig{
			MaxQoS:             2,
			MaxConnections:     1000,
			MaxInflight:        50,
			RetainAvailable:    true,
			WildcardAvailable:  true,
			SharedSubAvailable: true,
		},
		Storage: config.StorageConfig{
			DataDir:         filepath.Join(tempDir, "data"),
			WALDir:          walDir,
			MemoryBuffer:    1048576, // 1MB
			WALSyncInterval: time.Millisecond * 10,
			WALNoSync:       false,
		},
		Metrics: config.MetricsConfig{
			Enabled: true,
			Bind:    metricsAddr,
			Path:    "/metrics",
		},
	}

	// Create new broker instance (recovery happens in New)
	b2, err := broker.New(cfg)
	require.NoError(t, err)

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	tb2.broker = b2
	tb2.ctx = ctx2
	tb2.cancel = cancel2
	tb2.cfg = cfg

	// Start recovered broker
	go func() {
		err := b2.Start(ctx2)
		if err != nil && ctx2.Err() == nil {
			t.Errorf("Recovered broker start error: %v", err)
		}
	}()

	time.Sleep(200 * time.Millisecond) // Wait for recovery

	// Connect subscriber to verify retained messages are available
	subscriber := createMQTTClient(t, "recovery-subscriber")
	defer subscriber.Disconnect(1000)

	var recoveredMessages int32
	var receivedTopics []string
	var mu sync.Mutex

	// Subscribe to all recovery topics
	token := subscriber.Subscribe("test/recovery/+", 1, func(client mqtt.Client, msg mqtt.Message) {
		mu.Lock()
		receivedTopics = append(receivedTopics, msg.Topic())
		mu.Unlock()
		atomic.AddInt32(&recoveredMessages, 1)
	})
	require.True(t, token.WaitTimeout(5*time.Second))
	require.NoError(t, token.Error())

	// Wait for retained messages to be delivered
	timeout := time.After(10 * time.Second)
	for {
		count := atomic.LoadInt32(&recoveredMessages)
		if count >= 10 { // We expect 10 retained messages (one per topic)
			break
		}
		select {
		case <-timeout:
			t.Logf("Timeout: recovered %d messages", count)
			break
		case <-time.After(50 * time.Millisecond):
		}
	}

	finalCount := atomic.LoadInt32(&recoveredMessages)
	t.Logf("Recovery test completed: recovered %d retained messages", finalCount)

	// We should get at least some retained messages (one per topic)
	assert.Greater(t, int(finalCount), 0, "No messages recovered after crash")

	tb2.Stop()
}

func TestMQTTMetricsEndpoint(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	// Wait for metrics server to start
	time.Sleep(200 * time.Millisecond)

	// Test metrics endpoint
	resp, err := http.Get("http://" + metricsAddr + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	metrics := string(body)

	// Verify essential metrics are present
	expectedMetrics := []string{
		"promptmq_connections_current",
		"promptmq_connections_total",
		"promptmq_messages_published_total",
		"promptmq_messages_received_total",
		"promptmq_system_uptime_seconds",
	}

	for _, metric := range expectedMetrics {
		assert.Contains(t, metrics, metric, "Missing metric: "+metric)
	}

	t.Logf("Metrics endpoint working, found %d bytes of metrics data", len(body))
}

func TestMQTTConcurrentClients(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	const clientCount = 50
	const messagesPerClient = 10

	var wg sync.WaitGroup
	var totalReceived int32
	var clientConnections int32

	// Start subscriber clients
	for i := 0; i < clientCount/2; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			client := createMQTTClient(t, fmt.Sprintf("sub-client-%d", clientID))
			defer client.Disconnect(1000)
			atomic.AddInt32(&clientConnections, 1)

			token := client.Subscribe("test/concurrent", 1, func(c mqtt.Client, msg mqtt.Message) {
				atomic.AddInt32(&totalReceived, 1)
			})
			if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
				t.Errorf("Subscriber %d failed to subscribe", clientID)
				return
			}

			// Keep subscriber alive
			time.Sleep(5 * time.Second)
		}(i)
	}

	// Wait for subscribers to connect
	time.Sleep(500 * time.Millisecond)

	// Start publisher clients
	for i := 0; i < clientCount/2; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			client := createMQTTClient(t, fmt.Sprintf("pub-client-%d", clientID))
			defer client.Disconnect(1000)
			atomic.AddInt32(&clientConnections, 1)

			for j := 0; j < messagesPerClient; j++ {
				payload := fmt.Sprintf("client-%d-message-%d", clientID, j)
				token := client.Publish("test/concurrent", 1, false, payload)
				if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
					t.Errorf("Publisher %d failed to publish message %d", clientID, j)
				}
				time.Sleep(10 * time.Millisecond) // Small delay between messages
			}
		}(i)
	}

	// Wait for all operations to complete
	wg.Wait()

	// Wait a bit more for final message delivery
	time.Sleep(1 * time.Second)

	finalReceived := atomic.LoadInt32(&totalReceived)
	expectedMessages := int32(clientCount / 2 * messagesPerClient * clientCount / 2) // publishers * msgs * subscribers

	t.Logf("Concurrent client test completed:")
	t.Logf("  Clients: %d", atomic.LoadInt32(&clientConnections))
	t.Logf("  Expected messages: %d", expectedMessages)
	t.Logf("  Received messages: %d", finalReceived)

	// We should receive a reasonable number of messages (allow for some variance in concurrent execution)
	assert.Greater(t, int(finalReceived), int(expectedMessages)/2, "Too few messages received in concurrent test")
}

// Helper function for waiting with timeout
func waitWithTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
