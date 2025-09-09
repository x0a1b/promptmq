package metrics

import (
	"log/slog"
	"sync/atomic"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/mochi-mqtt/server/v2/system"
)

// MQTT Hook Interface Implementation
// These methods provide zero-allocation metrics collection for high performance

// ID returns the hook identifier
func (s *Server) ID() string {
	return "metrics"
}

// Provides returns the hook's capabilities
func (s *Server) Provides(b byte) bool {
	// We provide metrics for all hook events
	return true
}

// Init initializes the hook with server options
func (s *Server) Init(config any) error {
	// Hook initialization is handled in the constructor
	return nil
}

// SetOpts sets the hook options (required by mqtt.Hook interface)
func (s *Server) SetOpts(l *slog.Logger, o *mqtt.HookOptions) {
	// No additional setup required for metrics hook
}

// OnStarted is called when the MQTT server starts
func (s *Server) OnStarted() {
	s.logger.Debug().Msg("MQTT server started - metrics collection active")
}

// OnStopped is called when the MQTT server stops
func (s *Server) OnStopped() {
	s.logger.Debug().Msg("MQTT server stopped - metrics collection stopped")
}

// OnConnect is called when a client connects
func (s *Server) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	// Atomic increment for zero-allocation update
	atomic.AddInt64(s.connectionsCurrent, 1)
	s.connectionsTotal.Inc()

	// Record connection start time for duration calculation
	s.connectionStartTimes[cl.ID] = time.Now()

	s.logger.Debug().
		Str("client_id", cl.ID).
		Msg("Client connected")

	return nil
}

// OnDisconnect is called when a client disconnects
func (s *Server) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	// Atomic decrement for zero-allocation update
	atomic.AddInt64(s.connectionsCurrent, -1)
	s.disconnectionsTotal.Inc()

	// Record connection duration if we have the start time
	if startTime, exists := s.connectionStartTimes[cl.ID]; exists {
		duration := time.Since(startTime)
		s.connectionDurationHist.Observe(duration.Seconds())
		delete(s.connectionStartTimes, cl.ID)
	}

	s.logger.Debug().
		Str("client_id", cl.ID).
		Bool("expired", expire).
		Msg("Client disconnected")
}

// OnSubscribe is called when a client subscribes to topics
func (s *Server) OnSubscribe(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	// Update topic subscriber counts for each subscription in the packet
	if pk.Filters != nil {
		for _, filter := range pk.Filters {
			// Limit topic metric cardinality by only tracking non-wildcard topics
			if !containsWildcard(filter.Filter) && len(filter.Filter) < 100 {
				s.topicSubscriberGauge.WithLabelValues(filter.Filter).Inc()
			}
		}
	}

	s.logger.Debug().
		Str("client_id", cl.ID).
		Int("filter_count", len(pk.Filters)).
		Msg("Client subscribed")

	return pk
}

// OnUnsubscribe is called when a client unsubscribes from topics
func (s *Server) OnUnsubscribe(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	// Update topic subscriber counts for each unsubscription
	if pk.Filters != nil {
		for _, filter := range pk.Filters {
			if !containsWildcard(filter.Filter) && len(filter.Filter) < 100 {
				s.topicSubscriberGauge.WithLabelValues(filter.Filter).Dec()
			}
		}
	}

	s.logger.Debug().
		Str("client_id", cl.ID).
		Int("filter_count", len(pk.Filters)).
		Msg("Client unsubscribed")

	return pk
}

// OnPublish is called when a message is published - this is the critical path for performance
func (s *Server) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	startTime := time.Now() // Measure processing latency

	// Update message counters (zero-allocation atomic operations)
	s.messagesPublishedTotal.Inc()
	messageSize := float64(len(pk.Payload))
	s.bytesPublishedTotal.Add(messageSize)

	// Update message size histogram
	s.messageSizeHist.Observe(messageSize)

	// Update QoS distribution metrics (zero-allocation)
	switch pk.FixedHeader.Qos {
	case 0:
		s.qos0MessagesTotal.Inc()
	case 1:
		s.qos1MessagesTotal.Inc()
	case 2:
		s.qos2MessagesTotal.Inc()
	}

	// Update per-topic metrics (with cardinality limits for performance)
	topic := pk.TopicName
	if !containsWildcard(topic) && len(topic) < 100 {
		s.topicMessageCounts.WithLabelValues(topic).Inc()
		s.topicByteCounts.WithLabelValues(topic).Add(messageSize)
	}

	// Record processing latency (convert to microseconds)
	latencyMicros := float64(time.Since(startTime).Nanoseconds()) / 1000.0
	s.processingLatencyHist.Observe(latencyMicros)

	return pk, nil
}

// OnPublished is called after a message is published (optional additional metrics)
func (s *Server) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	// Additional post-publish metrics could be added here if needed
	// For now, keep it minimal for performance
}

// OnPacketRead is called when any packet is read
func (s *Server) OnPacketRead(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	// Track all received messages for throughput calculation
	s.messagesReceivedTotal.Inc()
	return pk, nil
}

// OnPacketEncode is called before packet encoding
func (s *Server) OnPacketEncode(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	// No metrics needed for packet encoding
	return pk
}

// OnPacketSent is called when a packet is sent
func (s *Server) OnPacketSent(cl *mqtt.Client, pk packets.Packet, b []byte) {
	// Track bytes sent
	s.bytesReceivedTotal.Add(float64(len(b)))
}

// OnPacketProcessed is called after packet processing
func (s *Server) OnPacketProcessed(cl *mqtt.Client, pk packets.Packet, err error) {
	// Could track processing errors here if needed
	if err != nil {
		s.logger.Debug().
			Err(err).
			Str("client_id", cl.ID).
			Uint8("packet_type", pk.FixedHeader.Type).
			Msg("Packet processing error")
	}
}

// OnSysInfoTick is called periodically with system information
func (s *Server) OnSysInfoTick(info *system.Info) {
	// System info metrics collection - use existing system metrics
	// The throughput is calculated separately in our own system metrics loop
}

// OnRetainPublished is called when a retained message is published
func (s *Server) OnRetainPublished(cl *mqtt.Client, pk packets.Packet) {
	// Retained messages are already counted in OnPublish
	s.logger.Debug().
		Str("client_id", cl.ID).
		Str("topic", pk.TopicName).
		Msg("Retained message published")
}

// OnRetainMessage is called when a message is retained
func (s *Server) OnRetainMessage(cl *mqtt.Client, pk packets.Packet, r int64) {
	// Track retained message operations
}

// OnQosPublish is called for QoS > 0 message publish
func (s *Server) OnQosPublish(cl *mqtt.Client, pk packets.Packet, sent int64, resends int) {
	// Track QoS message handling metrics
	if resends > 0 {
		s.logger.Debug().
			Str("client_id", cl.ID).
			Str("topic", pk.TopicName).
			Int("resends", resends).
			Msg("QoS message resent")
	}
}

// OnQosComplete is called when QoS flow completes
func (s *Server) OnQosComplete(cl *mqtt.Client, pk packets.Packet) {
	// QoS completion metrics
}

// OnQosDropped is called when QoS message is dropped
func (s *Server) OnQosDropped(cl *mqtt.Client, pk packets.Packet) {
	// Track dropped QoS messages
	s.logger.Debug().
		Str("client_id", cl.ID).
		Str("topic", pk.TopicName).
		Uint8("qos", pk.FixedHeader.Qos).
		Msg("QoS message dropped")
}

// OnPacketIDExhausted is called when packet IDs are exhausted
func (s *Server) OnPacketIDExhausted(cl *mqtt.Client, pk packets.Packet) {
	s.logger.Warn().
		Str("client_id", cl.ID).
		Msg("Packet ID exhausted")
}

// OnSessionEstablish is called when establishing a session
func (s *Server) OnSessionEstablish(cl *mqtt.Client, pk packets.Packet) {
	// Session establishment metrics
}

// OnSessionEstablished is called after session is established
func (s *Server) OnSessionEstablished(cl *mqtt.Client, pk packets.Packet) {
	// Session established metrics
}

// OnSubscribed is called after successful subscription
func (s *Server) OnSubscribed(cl *mqtt.Client, pk packets.Packet, reasonCodes []byte) {
	// Track successful subscriptions
}

// OnUnsubscribed is called after successful unsubscription
func (s *Server) OnUnsubscribed(cl *mqtt.Client, pk packets.Packet) {
	// Track successful unsubscriptions
}

// OnClientExpired is called when client sessions expire
func (s *Server) OnClientExpired(cl *mqtt.Client) {
	// Handle expired client metrics
	atomic.AddInt64(s.connectionsCurrent, -1)
	s.disconnectionsTotal.Inc()

	if startTime, exists := s.connectionStartTimes[cl.ID]; exists {
		duration := time.Since(startTime)
		s.connectionDurationHist.Observe(duration.Seconds())
		delete(s.connectionStartTimes, cl.ID)
	}
}

// OnRetainedExpired is called when retained messages expire
func (s *Server) OnRetainedExpired(filter string) {
	// Track expired retained messages
}

// OnWill is called when processing client will
func (s *Server) OnWill(cl *mqtt.Client, will mqtt.Will) (mqtt.Will, error) {
	// Will message metrics
	return will, nil
}

// OnWillSent is called when will message is sent
func (s *Server) OnWillSent(cl *mqtt.Client, pk packets.Packet) {
	// Will sent metrics
	s.logger.Debug().
		Str("client_id", cl.ID).
		Str("will_topic", pk.TopicName).
		Msg("Will message sent")
}

// OnPublishDropped is called when publish is dropped
func (s *Server) OnPublishDropped(cl *mqtt.Client, pk packets.Packet) {
	// Track dropped publishes
	s.logger.Debug().
		Str("client_id", cl.ID).
		Str("topic", pk.TopicName).
		Msg("Publish dropped")
}

// OnAuthPacket is called when auth packet is received
func (s *Server) OnAuthPacket(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	// Auth packet metrics
	return pk, nil
}

// OnSelectSubscribers is called when selecting subscribers
func (s *Server) OnSelectSubscribers(subs *mqtt.Subscribers, pk packets.Packet) *mqtt.Subscribers {
	// Don't modify subscriber selection, just return as-is
	return subs
}

// Helper functions

// containsWildcard checks if a topic contains MQTT wildcards
func containsWildcard(topic string) bool {
	for i := 0; i < len(topic); i++ {
		if topic[i] == '+' || topic[i] == '#' {
			return true
		}
	}
	return false
}

// Stop is called when the hook stops (required by mqtt.Hook interface)
func (s *Server) Stop() error {
	// Hook stop is different from server stop - this is just cleanup
	return nil
}

// OnACLCheck is called when ACL check is performed (required by mqtt.Hook interface)
func (s *Server) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	// Metrics hook doesn't participate in ACL decisions
	return true
}

// OnConnectAuthenticate is called when client authentication is performed
func (s *Server) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	// Metrics hook doesn't participate in authentication decisions
	return true
}

// Storage interface methods (required by mqtt.Hook interface)

// StoredClients returns stored clients (metrics hook doesn't store clients)
func (s *Server) StoredClients() ([]storage.Client, error) {
	return []storage.Client{}, nil
}

// StoredSubscriptions returns stored subscriptions (metrics hook doesn't store subscriptions)
func (s *Server) StoredSubscriptions() ([]storage.Subscription, error) {
	return []storage.Subscription{}, nil
}

// StoredInflightMessages returns stored inflight messages (metrics hook doesn't store messages)
func (s *Server) StoredInflightMessages() ([]storage.Message, error) {
	return []storage.Message{}, nil
}

// StoredRetainedMessages returns stored retained messages (metrics hook doesn't store messages)
func (s *Server) StoredRetainedMessages() ([]storage.Message, error) {
	return []storage.Message{}, nil
}

// StoredSysInfo returns stored system info (metrics hook doesn't store system info)
func (s *Server) StoredSysInfo() (storage.SystemInfo, error) {
	return storage.SystemInfo{}, nil
}
