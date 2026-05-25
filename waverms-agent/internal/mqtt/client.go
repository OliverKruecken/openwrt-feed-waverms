package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"

	"github.com/eclipse/paho.golang/paho"
)

// MessageHandler is a callback invoked when a message arrives on a subscribed topic.
type MessageHandler func(topic string, payload []byte)

// LWT carries Last-Will-Testament settings for the CONNECT packet.
type LWT struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

// ConnectOptions collects all parameters for Connect.
type ConnectOptions struct {
	BrokerHost string
	BrokerPort int
	ClientID   string
	Username   string
	Password   string
	LWT        *LWT
	TLSConfig  *tls.Config
}

// MQTTClient abstracts the MQTT connection so agent logic and tests
// can use the same interface.
type MQTTClient interface {
	Connect(ctx context.Context, opts ConnectOptions) error
	Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error
	Subscribe(ctx context.Context, topic string, qos byte, handler MessageHandler) error
	Disconnect()
	// Disconnected returns a channel that is closed when the connection is lost unexpectedly.
	Disconnected() <-chan struct{}
}

// PahoClient wraps github.com/eclipse/paho.golang/paho and implements MQTTClient.
type PahoClient struct {
	tlsCfg *tls.Config

	mu        sync.Mutex
	client    *paho.Client
	router    *paho.StandardRouter
	disconnCh chan struct{}
	closeOnce *sync.Once
}

// NewPahoClient creates a PahoClient. Pass nil for tlsCfg to use plain TCP.
func NewPahoClient(tlsCfg *tls.Config) *PahoClient {
	return &PahoClient{tlsCfg: tlsCfg}
}

// Connect dials the broker, performs the MQTT CONNECT handshake, and starts the
// client's background workers. It must be called exactly once per session.
func (c *PahoClient) Connect(ctx context.Context, opts ConnectOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Fresh disconnect channel and once for each connection attempt.
	disconnCh := make(chan struct{})
	closeOnce := &sync.Once{}
	c.disconnCh = disconnCh
	c.closeOnce = closeOnce

	addr := net.JoinHostPort(opts.BrokerHost, fmt.Sprintf("%d", opts.BrokerPort))

	// opts.TLSConfig overrides the client-level config; fall back to the
	// PahoClient's own tlsCfg (set at construction time in main.go).
	tlsCfg := opts.TLSConfig
	if tlsCfg == nil {
		tlsCfg = c.tlsCfg
	}

	var conn net.Conn
	var err error
	if tlsCfg != nil {
		conn, err = tls.Dial("tcp", addr, tlsCfg)
	} else {
		conn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}

	router := paho.NewStandardRouter()
	c.router = router

	cfg := paho.ClientConfig{
		ClientID: opts.ClientID,
		Conn:     conn,
		Router:   router,
		OnClientError: func(e error) {
			closeOnce.Do(func() { close(disconnCh) })
		},
		OnServerDisconnect: func(*paho.Disconnect) {
			closeOnce.Do(func() { close(disconnCh) })
		},
	}

	client := paho.NewClient(cfg)
	c.client = client

	cp := &paho.Connect{
		ClientID:   opts.ClientID,
		CleanStart: true,
		KeepAlive:  30,
	}

	if opts.Username != "" {
		cp.Username = opts.Username
		cp.UsernameFlag = true
	}
	if opts.Password != "" {
		cp.Password = []byte(opts.Password)
		cp.PasswordFlag = true
	}

	if opts.LWT != nil {
		cp.WillMessage = &paho.WillMessage{
			Topic:   opts.LWT.Topic,
			Payload: opts.LWT.Payload,
			QoS:     opts.LWT.QoS,
			Retain:  opts.LWT.Retain,
		}
		cp.WillProperties = &paho.WillProperties{}
	}

	ca, err := client.Connect(ctx, cp)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mqtt connect: %w", err)
	}
	if ca.ReasonCode != 0 {
		conn.Close()
		return fmt.Errorf("mqtt connect rejected: reason code %d", ca.ReasonCode)
	}

	return nil
}

// Publish sends a message to topic.
func (c *PahoClient) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil {
		return fmt.Errorf("not connected")
	}

	_, err := client.Publish(ctx, &paho.Publish{
		QoS:     qos,
		Retain:  retain,
		Topic:   topic,
		Payload: payload,
	})
	return err
}

// Subscribe registers handler for all messages arriving on topic.
func (c *PahoClient) Subscribe(ctx context.Context, topic string, qos byte, handler MessageHandler) error {
	c.mu.Lock()
	client := c.client
	router := c.router
	c.mu.Unlock()

	if client == nil {
		return fmt.Errorf("not connected")
	}

	router.RegisterHandler(topic, func(pub *paho.Publish) {
		handler(pub.Topic, pub.Payload)
	})

	_, err := client.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{
			{Topic: topic, QoS: qos},
		},
	})
	return err
}

// Disconnect sends a clean DISCONNECT to the broker.
func (c *PahoClient) Disconnect() {
	c.mu.Lock()
	client := c.client
	closeOnce := c.closeOnce
	disconnCh := c.disconnCh
	c.mu.Unlock()

	if client == nil {
		return
	}
	_ = client.Disconnect(&paho.Disconnect{ReasonCode: 0})
	// Close the disconnect channel so callers waiting on it unblock.
	if closeOnce != nil {
		closeOnce.Do(func() { close(disconnCh) })
	}
}

// Disconnected returns a channel closed when the connection is lost unexpectedly.
func (c *PahoClient) Disconnected() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disconnCh
}

// PublishedMsg records a single Publish call on MockMQTTClient.
type PublishedMsg struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

// MockMQTTClient is a test double that records all interactions.
type MockMQTTClient struct {
	mu             sync.Mutex
	ConnectOpts    []ConnectOptions
	Published      []PublishedMsg
	Subscriptions  map[string]MessageHandler
	disconnCh      chan struct{}
	connectErr     error
	subscribeErr   error // returned by every Subscribe call when set
	disconnectCnt  int  // incremented by every Disconnect call
}

// NewMockMQTTClient creates a ready-to-use mock.
func NewMockMQTTClient() *MockMQTTClient {
	return &MockMQTTClient{
		Subscriptions: make(map[string]MessageHandler),
		disconnCh:     make(chan struct{}),
	}
}

// SetConnectErr causes Connect to return err.
func (m *MockMQTTClient) SetConnectErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connectErr = err
}

func (m *MockMQTTClient) Connect(_ context.Context, opts ConnectOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ConnectOpts = append(m.ConnectOpts, opts)
	if m.connectErr != nil {
		return m.connectErr
	}
	// Each successful Connect gets a fresh disconnect channel,
	// mirroring the behaviour of a real MQTT connection.
	m.disconnCh = make(chan struct{})
	return nil
}

func (m *MockMQTTClient) Publish(_ context.Context, topic string, payload []byte, qos byte, retain bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Published = append(m.Published, PublishedMsg{
		Topic:   topic,
		Payload: payload,
		QoS:     qos,
		Retain:  retain,
	})
	return nil
}

// SetSubscribeErr causes every Subscribe call to return err.
func (m *MockMQTTClient) SetSubscribeErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribeErr = err
}

func (m *MockMQTTClient) Subscribe(_ context.Context, topic string, _ byte, handler MessageHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscribeErr != nil {
		return m.subscribeErr
	}
	m.Subscriptions[topic] = handler
	return nil
}

// DisconnectCount returns the number of times Disconnect has been called.
func (m *MockMQTTClient) DisconnectCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disconnectCnt
}

func (m *MockMQTTClient) Disconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnectCnt++
}

func (m *MockMQTTClient) Disconnected() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disconnCh
}

// SimulateMessage delivers payload to the registered handler for topic.
func (m *MockMQTTClient) SimulateMessage(topic string, payload []byte) {
	m.mu.Lock()
	handler := m.Subscriptions[topic]
	m.mu.Unlock()
	if handler != nil {
		handler(topic, payload)
	}
}

// SimulateDisconnect closes the disconnect channel, triggering reconnect in the agent.
func (m *MockMQTTClient) SimulateDisconnect() {
	m.mu.Lock()
	ch := m.disconnCh
	m.mu.Unlock()

	select {
	case <-ch:
		// already closed
	default:
		close(ch)
	}
}

// HasSubscription returns true if the mock has a handler registered for topic.
func (m *MockMQTTClient) HasSubscription(topic string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.Subscriptions[topic]
	return ok
}

// ConnectCount returns the number of Connect calls made.
func (m *MockMQTTClient) ConnectCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ConnectOpts)
}

// Reset prepares the mock for a new connection attempt (new disconnCh).
func (m *MockMQTTClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnCh = make(chan struct{})
	m.Published = nil
	m.ConnectOpts = nil
	m.Subscriptions = make(map[string]MessageHandler)
}
