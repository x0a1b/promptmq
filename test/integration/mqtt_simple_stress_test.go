package integration

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
)

// SimpleStressTestResults holds the results of a simple stress test
type SimpleStressTestResults struct {
	TotalPublished   int64
	TotalReceived    int64
	PublishErrors    int64
	ConnectionErrors int64
	Duration         time.Duration
	PublishRate      float64
	ReceiveRate      float64
	MessageLossRate  float64
}

// TestMQTTSimpleStressSmall runs a simplified small-scale stress test
func TestMQTTSimpleStressSmall(t *testing.T) {
	tb := setupTestBroker(t)
	defer tb.Stop()

	const (
		publishers  = 10
		subscribers = 10
		messages    = 50
		topic       = "stress/test/simple"
	)

	results := runSimpleStressTest(t, publishers, subscribers, messages, topic)

	t.Logf("Simple stress test results:")
	t.Logf("  Publishers: %d, Subscribers: %d", publishers, subscribers)
	t.Logf("  Total Published: %d", results.TotalPublished)
	t.Logf("  Total Received: %d", results.TotalReceived)
	t.Logf("  Publish Rate: %.2f msg/s", results.PublishRate)
	t.Logf("  Receive Rate: %.2f msg/s", results.ReceiveRate)
	t.Logf("  Message Loss Rate: %.3f%%", results.MessageLossRate*100)
	t.Logf("  Duration: %v", results.Duration)

	// Basic assertions
	assert.Greater(t, results.TotalPublished, int64(0), "Should publish messages")
	assert.Greater(t, results.TotalReceived, int64(0), "Should receive messages")
	assert.Less(t, results.MessageLossRate, 0.20, "Message loss should be < 20%")
}

// TestMQTTSimpleStressMedium runs a medium-scale stress test
func TestMQTTSimpleStressMedium(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping medium stress test in short mode")
	}

	tb := setupTestBroker(t)
	defer tb.Stop()

	const (
		publishers  = 50
		subscribers = 50
		messages    = 100
		topic       = "stress/test/medium"
	)

	results := runSimpleStressTest(t, publishers, subscribers, messages, topic)

	t.Logf("Medium stress test results:")
	t.Logf("  Publishers: %d, Subscribers: %d", publishers, subscribers)
	t.Logf("  Total Published: %d", results.TotalPublished)
	t.Logf("  Total Received: %d", results.TotalReceived)
	t.Logf("  Publish Rate: %.2f msg/s", results.PublishRate)
	t.Logf("  Receive Rate: %.2f msg/s", results.ReceiveRate)
	t.Logf("  Message Loss Rate: %.3f%%", results.MessageLossRate*100)
	t.Logf("  Duration: %v", results.Duration)

	// Assertions for medium test
	assert.Greater(t, results.PublishRate, 100.0, "Should achieve > 100 msg/s")
	assert.Greater(t, results.ReceiveRate, 100.0, "Should receive > 100 msg/s")
	assert.Less(t, results.MessageLossRate, 0.10, "Message loss should be < 10%")
}

// TestMQTTSimpleStressLarge runs a large-scale stress test
func TestMQTTSimpleStressLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large stress test in short mode")
	}

	tb := setupTestBroker(t)
	defer tb.Stop()

	const (
		publishers  = 200
		subscribers = 200
		messages    = 200
		topic       = "stress/test/large"
	)

	results := runSimpleStressTest(t, publishers, subscribers, messages, topic)

	t.Logf("Large stress test results:")
	t.Logf("  Publishers: %d, Subscribers: %d", publishers, subscribers)
	t.Logf("  Total Published: %d", results.TotalPublished)
	t.Logf("  Total Received: %d", results.TotalReceived)
	t.Logf("  Publish Rate: %.2f msg/s", results.PublishRate)
	t.Logf("  Receive Rate: %.2f msg/s", results.ReceiveRate)
	t.Logf("  Message Loss Rate: %.3f%%", results.MessageLossRate*100)
	t.Logf("  Duration: %v", results.Duration)

	// Assertions for large test
	assert.Greater(t, results.PublishRate, 500.0, "Should achieve > 500 msg/s")
	assert.Greater(t, results.ReceiveRate, 500.0, "Should receive > 500 msg/s")
	assert.Less(t, results.MessageLossRate, 0.05, "Message loss should be < 5%")
}

// runSimpleStressTest executes a simplified stress test
func runSimpleStressTest(t *testing.T, publishers, subscribers, messagesPerPub int, topic string) SimpleStressTestResults {
	var publishedCount int64
	var receivedCount int64
	var publishErrors int64
	var connectionErrors int64

	startTime := time.Now()

	var wg sync.WaitGroup

	// Start subscribers first
	t.Logf("Starting %d subscribers...", subscribers)
	for i := 0; i < subscribers; i++ {
		wg.Add(1)
		go func(subID int) {
			defer wg.Done()

			client := createSimpleStressClient(t, fmt.Sprintf("simple-sub-%d", subID))
			if client == nil {
				atomic.AddInt64(&connectionErrors, 1)
				return
			}
			defer client.Disconnect(100)

			token := client.Subscribe(topic, 1, func(c mqtt.Client, msg mqtt.Message) {
				atomic.AddInt64(&receivedCount, 1)
			})

			if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
				t.Logf("Subscriber %d failed to subscribe: %v", subID, token.Error())
				return
			}

			// Keep subscriber alive during test
			time.Sleep(15 * time.Second)
		}(i)
	}

	// Wait for subscribers to be ready
	time.Sleep(500 * time.Millisecond)

	// Start publishers
	t.Logf("Starting %d publishers...", publishers)
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func(pubID int) {
			defer wg.Done()

			client := createSimpleStressClient(t, fmt.Sprintf("simple-pub-%d", pubID))
			if client == nil {
				atomic.AddInt64(&connectionErrors, 1)
				return
			}
			defer client.Disconnect(100)

			// Publish messages
			for j := 0; j < messagesPerPub; j++ {
				payload := fmt.Sprintf("stress-message-pub%d-msg%d", pubID, j)
				token := client.Publish(topic, 1, false, payload)
				if token.WaitTimeout(5*time.Second) && token.Error() == nil {
					atomic.AddInt64(&publishedCount, 1)
				} else {
					atomic.AddInt64(&publishErrors, 1)
				}

				// Small delay to prevent overwhelming
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Wait for all goroutines
	wg.Wait()

	// Give time for final message delivery
	time.Sleep(2 * time.Second)

	duration := time.Since(startTime)
	published := atomic.LoadInt64(&publishedCount)
	received := atomic.LoadInt64(&receivedCount)

	results := SimpleStressTestResults{
		TotalPublished:   published,
		TotalReceived:    received,
		PublishErrors:    atomic.LoadInt64(&publishErrors),
		ConnectionErrors: atomic.LoadInt64(&connectionErrors),
		Duration:         duration,
		PublishRate:      float64(published) / duration.Seconds(),
		ReceiveRate:      float64(received) / duration.Seconds(),
	}

	if published > 0 {
		results.MessageLossRate = float64(published-received) / float64(published)
		if results.MessageLossRate < 0 {
			results.MessageLossRate = 0
		}
	}

	return results
}

// createSimpleStressClient creates a simple MQTT client for stress testing
func createSimpleStressClient(t *testing.T, clientID string) mqtt.Client {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://" + testBrokerAddr)
	opts.SetClientID(clientID)
	opts.SetCleanSession(true)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetKeepAlive(30 * time.Second)

	client := mqtt.NewClient(opts)
	token := client.Connect()

	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Logf("Client %s connection failed: %v", clientID, token.Error())
		return nil
	}

	return client
}
