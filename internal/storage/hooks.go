package storage

import (
	"log/slog"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/mochi-mqtt/server/v2/system"
)

// Ensure Manager implements the Hook interface at compile time
var _ mqtt.Hook = (*Manager)(nil)

// Hook interface methods that we don't need for storage functionality
// These are implemented with minimal/no-op behavior

// Stop is called when the hook is stopped
func (m *Manager) Stop() error {
	// Hook stop is different from Manager stop - just return nil
	return nil
}

// SetOpts sets hook options
func (m *Manager) SetOpts(l *slog.Logger, o *mqtt.HookOptions) {
	// No-op for storage hook
}

// OnStarted is called when the server starts
func (m *Manager) OnStarted() {
	m.logger.Debug().Msg("MQTT server started")
}

// OnStopped is called when the server stops
func (m *Manager) OnStopped() {
	m.logger.Debug().Msg("MQTT server stopped")
}

// OnSysInfoTick is called on system info updates
func (m *Manager) OnSysInfoTick(info *system.Info) {
	// Storage doesn't need system info
}

// OnPacketRead is called when a packet is read
func (m *Manager) OnPacketRead(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	// Storage doesn't modify incoming packets
	return pk, nil
}

// OnPacketEncode is called before a packet is encoded
func (m *Manager) OnPacketEncode(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	// Storage doesn't modify outgoing packets
	return pk
}

// OnSubscribe is called when a client subscribes
func (m *Manager) OnSubscribe(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client subscribed")
	return pk
}

// OnUnsubscribe is called when a client unsubscribes
func (m *Manager) OnUnsubscribe(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client unsubscribed")
	return pk
}

// OnRetainPublished is called when a retained message is published
func (m *Manager) OnRetainPublished(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().
		Str("client_id", cl.ID).
		Str("topic", pk.TopicName).
		Int("payload_size", len(pk.Payload)).
		Bool("retain_flag", pk.FixedHeader.Retain).
		Msg("OnRetainPublished called")
		
	// If payload is empty, this is a delete retained message operation
	if len(pk.Payload) == 0 {
		m.deleteRetainedFromDB(pk.TopicName)
		m.logger.Debug().
			Str("client_id", cl.ID).
			Str("topic", pk.TopicName).
			Msg("Retained message deleted from SQLite")
		return
	}
	
	// Store retained message directly to SQLite
	msg := &Message{
		ID:        m.messageID.Add(1),
		Topic:     pk.TopicName,
		Payload:   pk.Payload,
		QoS:       pk.FixedHeader.Qos,
		Retain:    true,
		ClientID:  cl.ID,
		Timestamp: time.Now(),
	}
	
	// Persist to SQLite
	m.persistRetainedToDB(msg)
	
	m.logger.Debug().
		Str("client_id", cl.ID).
		Str("topic", pk.TopicName).
		Int("payload_size", len(pk.Payload)).
		Msg("Retained message published and stored")
}

// OnPacketIDExhausted is called when packet IDs are exhausted
func (m *Manager) OnPacketIDExhausted(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Packet ID exhausted")
}

// OnWill is called when a client will is processed
func (m *Manager) OnWill(cl *mqtt.Client, will mqtt.Will) (mqtt.Will, error) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client will processed")
	return will, nil
}

// OnWillSent is called when a will message is sent
func (m *Manager) OnWillSent(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Will message sent")
}

// Storage interface methods - these provide persistent storage capabilities
// For now, implement them as no-ops since we're not using Mochi's storage interface

// StoredClients returns stored clients
func (m *Manager) StoredClients() ([]storage.Client, error) {
	// We don't store client state persistently yet
	return []storage.Client{}, nil
}

// StoredSubscriptions returns stored subscriptions
func (m *Manager) StoredSubscriptions() ([]storage.Subscription, error) {
	// We don't store subscriptions persistently yet
	return []storage.Subscription{}, nil
}

// StoredInflightMessages returns stored inflight messages
func (m *Manager) StoredInflightMessages() ([]storage.Message, error) {
	// We don't store inflight messages persistently yet
	return []storage.Message{}, nil
}

// StoredRetainedMessages returns stored retained messages from SQLite
func (m *Manager) StoredRetainedMessages() ([]storage.Message, error) {
	m.logger.Debug().Msg("StoredRetainedMessages called - querying SQLite database")
	
	// Query retained messages from SQLite
	selectSQL := `SELECT topic, payload, qos, client_id, timestamp, msg_id FROM retained_messages`
	rows, err := m.db.Query(selectSQL)
	if err != nil {
		m.logger.Error().Err(err).Msg("Failed to query retained messages from SQLite")
		return []storage.Message{}, err
	}
	defer rows.Close()
	
	var messages []storage.Message
	for rows.Next() {
		var topic, clientID string
		var payload []byte
		var qos int
		var timestampNano, msgID int64
		
		err := rows.Scan(&topic, &payload, &qos, &clientID, &timestampNano, &msgID)
		if err != nil {
			m.logger.Error().Err(err).Msg("Failed to scan retained message row")
			continue
		}
		
		storageMsg := storage.Message{
			ID:        topic, // Use topic as ID for retained messages
			T:         "retained",
			Payload:   payload,
			TopicName: topic,
			Origin:    clientID,
			Created:   timestampNano / 1e9, // Convert nanoseconds to seconds
			Sent:      timestampNano / 1e9,
			PacketID:  uint16(msgID),
			FixedHeader: packets.FixedHeader{
				Type:   packets.Publish,
				Qos:    byte(qos),
				Retain: true,
			},
		}
		messages = append(messages, storageMsg)
		
		m.logger.Debug().
			Str("topic", topic).
			Str("client_id", clientID).
			Int("payload_size", len(payload)).
			Msg("Retrieved retained message from SQLite")
	}
	
	if err = rows.Err(); err != nil {
		m.logger.Error().Err(err).Msg("Error during retained message query iteration")
		return messages, err
	}
	
	m.logger.Debug().Int("retained_count", len(messages)).Msg("Retrieved retained messages from SQLite for MQTT server")
	return messages, nil
}

// StoredSysInfo returns stored system info
func (m *Manager) StoredSysInfo() (storage.SystemInfo, error) {
	// We don't store system info persistently yet
	return storage.SystemInfo{}, nil
}

// OnAuthPacket is called when an auth packet is received
func (m *Manager) OnAuthPacket(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Auth packet received")
	return pk, nil
}

// OnSessionEstablish is called when a session is established
func (m *Manager) OnSessionEstablish(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Session established")
}

// OnSessionEstablished is called after a session is established
func (m *Manager) OnSessionEstablished(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Session established")
}

// OnPacketSent is called when a packet is sent
func (m *Manager) OnPacketSent(cl *mqtt.Client, pk packets.Packet, b []byte) {
	// Storage manager doesn't need to track sent packets
}

// OnPacketProcessed is called after a packet is processed
func (m *Manager) OnPacketProcessed(cl *mqtt.Client, pk packets.Packet, err error) {
	// Storage manager doesn't need post-processing hooks
}

// OnRetainMessage is called when a message is retained
func (m *Manager) OnRetainMessage(cl *mqtt.Client, pk packets.Packet, r int64) {
	m.logger.Debug().
		Str("client_id", cl.ID).
		Str("topic", pk.TopicName).
		Int("payload_size", len(pk.Payload)).
		Bool("retain_flag", pk.FixedHeader.Retain).
		Msg("OnRetainMessage called")
		
	// If payload is empty, this is a delete retained message operation
	if len(pk.Payload) == 0 {
		m.deleteRetainedFromDB(pk.TopicName)
		m.logger.Debug().
			Str("client_id", cl.ID).
			Str("topic", pk.TopicName).
			Msg("Retained message deleted from SQLite")
		return
	}
	
	// Store retained message directly to SQLite
	msg := &Message{
		ID:        m.messageID.Add(1),
		Topic:     pk.TopicName,
		Payload:   pk.Payload,
		QoS:       pk.FixedHeader.Qos,
		Retain:    true,
		ClientID:  cl.ID,
		Timestamp: time.Now(),
	}
	
	// Persist to SQLite
	m.persistRetainedToDB(msg)
	
	m.logger.Debug().
		Str("client_id", cl.ID).
		Str("topic", pk.TopicName).
		Int("payload_size", len(pk.Payload)).
		Msg("Retained message stored and persisted")
}

// OnQosPublish is called when a QoS > 0 message is published
func (m *Manager) OnQosPublish(cl *mqtt.Client, pk packets.Packet, sent int64, resends int) {
	m.logger.Debug().Str("client_id", cl.ID).Str("topic", pk.TopicName).Msg("QoS message published")
}

// OnQosComplete is called when a QoS flow is complete
func (m *Manager) OnQosComplete(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("QoS flow complete")
}

// OnQosDropped is called when a QoS message is dropped
func (m *Manager) OnQosDropped(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("QoS message dropped")
}

// OnClientExpired is called when client sessions expire
func (m *Manager) OnClientExpired(cl *mqtt.Client) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client session expired")
}

// OnRetainedExpired is called when retained messages expire
func (m *Manager) OnRetainedExpired(filter string) {
	m.logger.Debug().Str("filter", filter).Msg("Retained messages expired")
}

// OnPublishDropped is called when a publish is dropped
func (m *Manager) OnPublishDropped(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Str("topic", pk.TopicName).Msg("Publish dropped")
}

// OnSelectSubscribers is called when selecting subscribers for a message
func (m *Manager) OnSelectSubscribers(subs *mqtt.Subscribers, pk packets.Packet) *mqtt.Subscribers {
	// Storage doesn't modify subscriber selection, return as-is
	return subs
}

// OnSubscribed is called after a client subscribes
func (m *Manager) OnSubscribed(cl *mqtt.Client, pk packets.Packet, reasonCodes []byte) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client subscribed successfully")
}

// OnUnsubscribed is called after a client unsubscribes
func (m *Manager) OnUnsubscribed(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client unsubscribed successfully")
}
