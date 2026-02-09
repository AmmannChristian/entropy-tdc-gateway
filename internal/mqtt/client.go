// Package mqtt provides a receive-only MQTT client tailored for consuming
// TDC timestamp events published by the Raspberry Pi data acquisition system.
// It wraps the Eclipse Paho library, handles automatic reconnection and
// resubscription, and supports optional TLS transport.
package mqtt

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"entropy-tdc-gateway/internal/clock"
	"entropy-tdc-gateway/internal/metrics"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Handler receives decoded MQTT messages. Implementations should return
// promptly; expensive processing should be offloaded to a separate goroutine.
type Handler interface {
	OnMessage(topic string, payload []byte)
}

// Config holds the parameters required to connect to an MQTT broker and
// subscribe to one or more TDC timestamp topics.
type Config struct {
	BrokerURL string   // e.g., "tcp://127.0.0.1:1883" or "ssl://mqtt.example.com:8883"
	ClientID  string   // optional; if empty, a random ID is generated
	Topics    []string // topic filters to subscribe, e.g., ["timestamps/channel/1", "timestamps/channel/2"]
	QoS       byte     // 0 or 1 (use 0 for the lowest latency on LAN)
	Username  string   // optional; MQTT username for authentication
	Password  string   // optional; MQTT password for authentication
	TLSCAFile string   // optional; path to CA certificate file for TLS verification
}

// Client is a receive-only MQTT client built on the Eclipse Paho library.
// It subscribes to the configured topics on connect and automatically
// resubscribes after reconnections.
type Client struct {
	config                    Config
	pahoClient                paho.Client
	handler                   Handler
	initialSubscriptionOnce   sync.Once
	initialSubscriptionResult chan error
	connectAttempts           int32
	clockSource               clock.Clock
}

// NewClient validates the configuration and constructs an MQTT client. The
// underlying Paho client is created but the TCP connection is not opened
// until Connect is called. When ClientID is empty a random UUID-based
// identifier is generated.
func NewClient(config Config, handler Handler) (*Client, error) {
	if config.BrokerURL == "" {
		return nil, errors.New("mqtt: BrokerURL required")
	}
	if len(config.Topics) == 0 {
		return nil, errors.New("mqtt: at least one Topic required")
	}
	if config.ClientID == "" {
		generatedID, err := generateClientID()
		if err != nil {
			return nil, fmt.Errorf("mqtt: generate client id: %w", err)
		}

		config.ClientID = generatedID
	}
	if config.QoS > 1 {
		config.QoS = 1
	}

	client := &Client{
		config:                    config,
		handler:                   handler,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               clock.RealClock{},
	}

	opts := paho.NewClientOptions().
		AddBroker(config.BrokerURL).
		SetClientID(config.ClientID).
		SetAutoReconnect(true). // pragmatic default on a LAN
		SetCleanSession(false).
		SetKeepAlive(20 * time.Second).
		SetPingTimeout(5 * time.Second).
		SetDefaultPublishHandler(func(_ paho.Client, msg paho.Message) {
			if handler != nil {
				handler.OnMessage(msg.Topic(), msg.Payload())
			}
		}).
		SetOnConnectHandler(func(pc paho.Client) {
			client.handleConnect(pc)
		}).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			metrics.SetMQTTConnected(false)
			metrics.RecordMQTTDisconnect()
			if err != nil {
				log.Printf("mqtt: connection lost: %v", err)
			} else {
				log.Printf("mqtt: connection lost (reason unknown)")
			}
		})

	if config.Username != "" {
		opts.SetUsername(config.Username)
	}
	if config.Password != "" {
		opts.SetPassword(config.Password)
	}

	if isTLSBroker(config.BrokerURL) {
		tlsConfig, err := createMQTTTLSConfig(config)
		if err != nil {
			return nil, fmt.Errorf("mqtt: TLS configuration failed: %w", err)
		}
		opts.SetTLSConfig(tlsConfig)
	}

	client.pahoClient = paho.NewClient(opts)
	return client, nil
}

// isTLSBroker reports whether the broker URL scheme implies a TLS transport.
func isTLSBroker(brokerURL string) bool {
	lower := strings.ToLower(brokerURL)
	return strings.HasPrefix(lower, "ssl://") ||
		strings.HasPrefix(lower, "tls://") ||
		strings.HasPrefix(lower, "mqtts://") ||
		strings.HasPrefix(lower, "tcps://")
}

// createMQTTTLSConfig builds a tls.Config using either the custom CA
// certificate specified in Config.TLSCAFile or the system certificate pool.
func createMQTTTLSConfig(config Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if config.TLSCAFile != "" {
		caCert, err := os.ReadFile(config.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA certificate: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, errors.New("failed to parse CA certificate")
		}

		tlsConfig.RootCAs = caCertPool
		log.Printf("mqtt: using custom CA certificate from %s", config.TLSCAFile)
	} else {
		systemCAs, err := x509.SystemCertPool()
		if err != nil {
			log.Printf("mqtt: warning,failed to load system CA pool: %v, using empty pool", err)
			systemCAs = x509.NewCertPool()
		}
		tlsConfig.RootCAs = systemCAs
		log.Println("mqtt: using system CA certificate pool")
	}

	return tlsConfig, nil
}

// generateClientID produces a cryptographically random client identifier in
// the form "entropy-rx-<UUIDv4>".
func generateClientID() (string, error) {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", err
	}

	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 10

	encoded := make([]byte, hex.EncodedLen(len(uuid)))
	hex.Encode(encoded, uuid[:])

	return fmt.Sprintf(
		"entropy-rx-%s-%s-%s-%s-%s",
		encoded[0:8],
		encoded[8:12],
		encoded[12:16],
		encoded[16:20],
		encoded[20:32],
	), nil
}

// Connect opens the TCP connection and blocks until the initial topic
// subscription completes or a ten-second timeout elapses.
func (c *Client) Connect() error {
	if c.pahoClient == nil {
		return errors.New("mqtt: client not initialized")
	}

	token := c.pahoClient.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		metrics.SetMQTTConnected(false)
		return errors.New("mqtt: connect timeout")
	}

	if err := token.Error(); err != nil {
		metrics.SetMQTTConnected(false)
		return fmt.Errorf("mqtt: connect failed: %w", err)
	}

	select {
	case err, ok := <-c.initialSubscriptionResult:
		if !ok || err == nil {
			return nil
		}
		metrics.SetMQTTConnected(false)
		return err
	case <-c.afterDuration(10 * time.Second):
		metrics.SetMQTTConnected(false)
		return errors.New("mqtt: initial subscribe timeout")
	}
}

func (c *Client) afterDuration(d time.Duration) <-chan time.Time {
	if c.clockSource == nil {
		return time.After(d)
	}
	return c.clockSource.After(d)
}

// Close disconnects from the broker with a 250 ms quiesce period.
func (c *Client) Close() {
	metrics.SetMQTTConnected(false)

	if c.pahoClient != nil && c.pahoClient.IsConnectionOpen() {
		metrics.RecordMQTTDisconnect()
		c.pahoClient.Disconnect(250) // ms
	}
}

// handleConnect resubscribes to all configured topics on every connection
// (including reconnections) and signals completion of the initial subscription.
func (c *Client) handleConnect(pahoClient paho.Client) {
	if err := c.subscribe(pahoClient); err != nil {
		metrics.SetMQTTConnected(false)
		log.Printf("mqtt: subscribe failed: %v", err)
		c.completeInitialSubscription(fmt.Errorf("mqtt: subscribe failed: %w", err))
		return
	}

	if atomic.AddInt32(&c.connectAttempts, 1) > 1 {
		metrics.RecordMQTTReconnect()
		log.Printf("mqtt: re-subscribed to %v (QoS=%d)", c.config.Topics, c.config.QoS)
	} else {
		log.Printf("mqtt: subscribed to %v (QoS=%d)", c.config.Topics, c.config.QoS)
	}

	metrics.SetMQTTConnected(true)
	metrics.RecordMQTTConnect()
	c.completeInitialSubscription(nil)
}

// subscribe issues one subscription per configured topic and blocks until
// each is acknowledged or times out.
func (c *Client) subscribe(pahoClient paho.Client) error {
	for _, topic := range c.config.Topics {
		token := pahoClient.Subscribe(topic, c.config.QoS, nil)
		if !token.WaitTimeout(10 * time.Second) {
			return fmt.Errorf("subscribe to %s: timeout", topic)
		}
		if err := token.Error(); err != nil {
			return fmt.Errorf("subscribe to %s: %w", topic, err)
		}
	}
	return nil
}

// completeInitialSubscription delivers the result of the first subscription
// attempt exactly once, unblocking the Connect caller.
func (c *Client) completeInitialSubscription(err error) {
	c.initialSubscriptionOnce.Do(func() {
		c.initialSubscriptionResult <- err
		close(c.initialSubscriptionResult)
	})
}
