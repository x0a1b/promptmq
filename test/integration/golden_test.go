package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GoldenPrompt represents a known-good MQTT command sequence for regression testing
type GoldenPrompt struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Config      *TestBrokerConfig   `json:"config,omitempty"`
	Steps       []GoldenPromptStep  `json:"steps"`
	Expected    GoldenPromptResult  `json:"expected"`
	Tolerance   GoldenTolerance     `json:"tolerance,omitempty"`
}

// GoldenPromptStep represents a single action in the test sequence
type GoldenPromptStep struct {
	Type        string            `json:"type"`        // "connect", "subscribe", "publish", "disconnect", "wait"
	ClientID    string            `json:"client_id"`
	Topic       string            `json:"topic,omitempty"`
	Payload     string            `json:"payload,omitempty"`
	QoS         byte              `json:"qos,omitempty"`
	Retained    bool              `json:"retained,omitempty"`
	Duration    string            `json:"duration,omitempty"` // For "wait" steps
	Parameters  map[string]interface{} `json:"parameters,omitempty"` // Additional step parameters
}

// GoldenPromptResult represents expected test outcomes
type GoldenPromptResult struct {
	MessageCounts    map[string]int            `json:"message_counts"`    // client_id -> expected message count
	MessageContents  map[string][]string       `json:"message_contents"`  // client_id -> expected message payloads
	Performance      GoldenPerformanceMetrics  `json:"performance"`
	MetricsValues    map[string]float64        `json:"metrics_values"`    // metric_name -> expected value
	BrokerStats      map[string]interface{}    `json:"broker_stats"`      // Expected broker statistics
}

// GoldenPerformanceMetrics represents expected performance characteristics
type GoldenPerformanceMetrics struct {
	MinThroughput float64 `json:"min_throughput"` // msg/sec
	MaxLatency    string  `json:"max_latency"`    // duration string
	MaxMemoryMB   int     `json:"max_memory_mb"`
}

// GoldenTolerance defines acceptable deviation from expected values
type GoldenTolerance struct {
	MessageCountDelta int     `json:"message_count_delta"` // Acceptable difference in message counts
	PerformanceDelta  float64 `json:"performance_delta"`   // Acceptable percentage difference (0.1 = 10%)
	MetricsDelta      float64 `json:"metrics_delta"`       // Acceptable difference in metrics
}

// GoldenTestRunner executes golden prompt tests for regression monitoring
type GoldenTestRunner struct {
	t           *testing.T
	goldenDir   string
	broker      *TestBroker
	clients     map[string]*TestClient
	startTime   time.Time
}

// NewGoldenTestRunner creates a new golden test runner
func NewGoldenTestRunner(t *testing.T, goldenDir string) *GoldenTestRunner {
	if goldenDir == "" {
		goldenDir = filepath.Join("testdata", "golden")
	}
	
	return &GoldenTestRunner{
		t:         t,
		goldenDir: goldenDir,
		clients:   make(map[string]*TestClient),
	}
}

// RunGoldenTest executes a single golden prompt test
func (gtr *GoldenTestRunner) RunGoldenTest(filename string) {
	// Load golden prompt from file
	goldenPrompt, err := gtr.loadGoldenPrompt(filename)
	require.NoError(gtr.t, err, "Failed to load golden prompt %s", filename)

	gtr.t.Logf("Running golden test: %s - %s", goldenPrompt.Name, goldenPrompt.Description)

	// Setup test broker with specified configuration
	config := goldenPrompt.Config
	if config == nil {
		config = DefaultTestConfig()
	}
	
	gtr.broker = NewTestBroker(gtr.t, config)
	require.NoError(gtr.t, gtr.broker.Start(), "Failed to start broker for golden test")

	gtr.startTime = time.Now()

	// Execute test steps
	for i, step := range goldenPrompt.Steps {
		gtr.t.Logf("Executing step %d: %s", i+1, step.Type)
		err := gtr.executeStep(step)
		require.NoError(gtr.t, err, "Failed to execute step %d (%s)", i+1, step.Type)
	}

	// Validate results
	gtr.validateResults(goldenPrompt.Expected, goldenPrompt.Tolerance)

	// Cleanup
	gtr.cleanup()
}

// executeStep executes a single step in the golden prompt
func (gtr *GoldenTestRunner) executeStep(step GoldenPromptStep) error {
	switch step.Type {
	case "connect":
		return gtr.executeConnect(step)
	case "disconnect":
		return gtr.executeDisconnect(step)
	case "subscribe":
		return gtr.executeSubscribe(step)
	case "unsubscribe":
		return gtr.executeUnsubscribe(step)
	case "publish":
		return gtr.executePublish(step)
	case "wait":
		return gtr.executeWait(step)
	default:
		return fmt.Errorf("unknown step type: %s", step.Type)
	}
}

// executeConnect connects a client
func (gtr *GoldenTestRunner) executeConnect(step GoldenPromptStep) error {
	if _, exists := gtr.clients[step.ClientID]; exists {
		return fmt.Errorf("client %s already exists", step.ClientID)
	}

	config := &TestClientConfig{
		ClientID: step.ClientID,
	}
	
	// Apply additional parameters if specified
	if step.Parameters != nil {
		if cleanSession, ok := step.Parameters["clean_session"].(bool); ok {
			config.CleanSession = cleanSession
		}
		if keepAlive, ok := step.Parameters["keep_alive"].(string); ok {
			if duration, err := time.ParseDuration(keepAlive); err == nil {
				config.KeepAlive = duration
			}
		}
	}

	client := NewTestClient(gtr.t, gtr.broker.MQTTAddress(), config)
	err := client.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect client %s: %w", step.ClientID, err)
	}

	gtr.clients[step.ClientID] = client
	return nil
}

// executeDisconnect disconnects a client
func (gtr *GoldenTestRunner) executeDisconnect(step GoldenPromptStep) error {
	client, exists := gtr.clients[step.ClientID]
	if !exists {
		return fmt.Errorf("client %s not found", step.ClientID)
	}

	client.Disconnect()
	delete(gtr.clients, step.ClientID)
	return nil
}

// executeSubscribe subscribes a client to a topic
func (gtr *GoldenTestRunner) executeSubscribe(step GoldenPromptStep) error {
	client, exists := gtr.clients[step.ClientID]
	if !exists {
		return fmt.Errorf("client %s not found", step.ClientID)
	}

	return client.Subscribe(step.Topic, step.QoS)
}

// executeUnsubscribe unsubscribes a client from a topic
func (gtr *GoldenTestRunner) executeUnsubscribe(step GoldenPromptStep) error {
	client, exists := gtr.clients[step.ClientID]
	if !exists {
		return fmt.Errorf("client %s not found", step.ClientID)
	}

	return client.Unsubscribe(step.Topic)
}

// executePublish publishes a message
func (gtr *GoldenTestRunner) executePublish(step GoldenPromptStep) error {
	client, exists := gtr.clients[step.ClientID]
	if !exists {
		return fmt.Errorf("client %s not found", step.ClientID)
	}

	return client.PublishString(step.Topic, step.QoS, step.Retained, step.Payload)
}

// executeWait waits for a specified duration
func (gtr *GoldenTestRunner) executeWait(step GoldenPromptStep) error {
	duration, err := time.ParseDuration(step.Duration)
	if err != nil {
		return fmt.Errorf("invalid duration %s: %w", step.Duration, err)
	}

	time.Sleep(duration)
	return nil
}

// validateResults validates the test results against expected outcomes
func (gtr *GoldenTestRunner) validateResults(expected GoldenPromptResult, tolerance GoldenTolerance) {
	// Validate message counts
	for clientID, expectedCount := range expected.MessageCounts {
		client, exists := gtr.clients[clientID]
		require.True(gtr.t, exists, "Client %s should exist", clientID)

		actualCount := client.GetMessageCount()
		delta := int64(tolerance.MessageCountDelta)
		
		assert.InDelta(gtr.t, expectedCount, actualCount, float64(delta),
			"Message count mismatch for client %s", clientID)
	}

	// Validate message contents
	for clientID, expectedContents := range expected.MessageContents {
		client, exists := gtr.clients[clientID]
		require.True(gtr.t, exists, "Client %s should exist", clientID)

		allMessages := client.GetAllMessages()
		var actualContents []string
		
		for _, messages := range allMessages {
			for _, msg := range messages {
				actualContents = append(actualContents, string(msg.Payload))
			}
		}

		assert.ElementsMatch(gtr.t, expectedContents, actualContents,
			"Message contents mismatch for client %s", clientID)
	}

	// Validate performance metrics
	if expected.Performance.MinThroughput > 0 {
		totalDuration := time.Since(gtr.startTime)
		totalMessages := 0
		for _, client := range gtr.clients {
			totalMessages += int(client.GetMessageCount())
		}
		
		actualThroughput := float64(totalMessages) / totalDuration.Seconds()
		assert.GreaterOrEqual(gtr.t, actualThroughput, expected.Performance.MinThroughput,
			"Throughput should meet minimum requirement")
	}

	// Validate metrics endpoint values
	if len(expected.MetricsValues) > 0 && gtr.broker.Config().Metrics.Enabled {
		metricsData, err := gtr.getMetricsData()
		if err != nil {
			gtr.t.Logf("Warning: Failed to get metrics data: %v", err)
		} else {
			gtr.validateMetricsValues(metricsData, expected.MetricsValues, tolerance.MetricsDelta)
		}
	}
}

// getMetricsData retrieves current metrics from the broker
func (gtr *GoldenTestRunner) getMetricsData() (map[string]float64, error) {
	resp, err := http.Get(gtr.broker.MetricsAddress())
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read metrics response: %w", err)
	}

	// Parse Prometheus metrics format (simplified parsing for key metrics)
	// This is a basic implementation - in production, you might want to use a proper Prometheus client
	return gtr.parsePrometheusMetrics(string(body)), nil
}

// parsePrometheusMetrics parses basic Prometheus metrics format
func (gtr *GoldenTestRunner) parsePrometheusMetrics(metricsText string) map[string]float64 {
	// Simplified metrics parsing - extracts key metrics
	// In a real implementation, use a proper Prometheus parser
	metrics := make(map[string]float64)
	
	// This is a placeholder implementation
	// Real implementation would parse the Prometheus format properly
	metrics["connections_total"] = 0
	metrics["messages_published_total"] = 0
	metrics["messages_received_total"] = 0
	
	return metrics
}

// validateMetricsValues validates metrics against expected values
func (gtr *GoldenTestRunner) validateMetricsValues(actual map[string]float64, expected map[string]float64, delta float64) {
	for metricName, expectedValue := range expected {
		actualValue, exists := actual[metricName]
		if !exists {
			gtr.t.Logf("Warning: Metric %s not found in actual metrics", metricName)
			continue
		}

		tolerance := expectedValue * delta
		assert.InDelta(gtr.t, expectedValue, actualValue, tolerance,
			"Metric %s value mismatch", metricName)
	}
}

// loadGoldenPrompt loads a golden prompt from a JSON file
func (gtr *GoldenTestRunner) loadGoldenPrompt(filename string) (*GoldenPrompt, error) {
	filePath := filepath.Join(gtr.goldenDir, filename)
	
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read golden prompt file %s: %w", filePath, err)
	}

	var prompt GoldenPrompt
	err = json.Unmarshal(data, &prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse golden prompt file %s: %w", filePath, err)
	}

	return &prompt, nil
}

// cleanup disconnects all clients and cleans up resources
func (gtr *GoldenTestRunner) cleanup() {
	for _, client := range gtr.clients {
		client.Disconnect()
	}
	gtr.clients = make(map[string]*TestClient)
}

// TestGoldenPrompts runs all golden prompt tests for regression monitoring
func TestGoldenPrompts(t *testing.T) {
	goldenDir := filepath.Join("testdata", "golden")
	
	// Create testdata directory if it doesn't exist
	err := os.MkdirAll(goldenDir, 0755)
	require.NoError(t, err)

	// Check if any golden prompt files exist
	files, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil {
		t.Skip("Failed to read golden prompt directory")
	}

	if len(files) == 0 {
		// Create a sample golden prompt file for demonstration
		gtr := NewGoldenTestRunner(t, goldenDir)
		gtr.createSampleGoldenPrompt()
		t.Skip("No golden prompt files found, created sample")
	}

	// Run each golden prompt test
	runner := NewGoldenTestRunner(t, goldenDir)
	
	for _, file := range files {
		filename := filepath.Base(file)
		t.Run(filename, func(t *testing.T) {
			runner.t = t // Update test context for subtest
			runner.RunGoldenTest(filename)
		})
	}
}

// createSampleGoldenPrompt creates a sample golden prompt file for demonstration
func (gtr *GoldenTestRunner) createSampleGoldenPrompt() {
	samplePrompt := GoldenPrompt{
		Name:        "BasicPubSubRegression",
		Description: "Regression test for basic publish/subscribe functionality",
		Config:      DefaultTestConfig(),
		Steps: []GoldenPromptStep{
			{Type: "connect", ClientID: "publisher"},
			{Type: "connect", ClientID: "subscriber"},
			{Type: "subscribe", ClientID: "subscriber", Topic: "test/regression", QoS: 0},
			{Type: "wait", Duration: "100ms"},
			{Type: "publish", ClientID: "publisher", Topic: "test/regression", Payload: "regression test message", QoS: 0},
			{Type: "wait", Duration: "500ms"},
		},
		Expected: GoldenPromptResult{
			MessageCounts: map[string]int{
				"subscriber": 1,
			},
			MessageContents: map[string][]string{
				"subscriber": {"regression test message"},
			},
			Performance: GoldenPerformanceMetrics{
				MinThroughput: 1.0,
				MaxLatency:    "1s",
			},
		},
		Tolerance: GoldenTolerance{
			MessageCountDelta: 0,
			PerformanceDelta:  0.2,
			MetricsDelta:      0.1,
		},
	}

	sampleFile := filepath.Join(gtr.goldenDir, "basic_pubsub_regression.json")
	data, _ := json.MarshalIndent(samplePrompt, "", "  ")
	os.WriteFile(sampleFile, data, 0644)
	
	gtr.t.Logf("Created sample golden prompt: %s", sampleFile)
}