package mqtt

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"entropy-tdc-gateway/internal/clock"
	testutil "entropy-tdc-gateway/testutil"

	paho "github.com/eclipse/paho.mqtt.golang"
)

func TestClientConnectWaitsForInitialSubscription(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{
		connectToken: &stubToken{waitTimeoutResult: true},
	}

	fakeClock := clock.NewFakeClock()

	client := &Client{
		config: Config{
			Topics: []string{"sr90/tdc/0"},
			QoS:    0,
		},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               fakeClock,
	}

	done := make(chan error, 1)
	go func() {
		done <- client.Connect()
	}()

	select {
	case err := <-done:
		t.Fatalf("Connect completed before subscription result: %v", err)
	default:
	}

	client.initialSubscriptionResult <- nil
	close(client.initialSubscriptionResult)

	if err := testutil.WaitForError(t, done, "Connect to complete after subscription result"); err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
}

func TestClientConnectPropagatesSubscriptionError(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{
		connectToken: &stubToken{waitTimeoutResult: true},
	}

	fakeClock := clock.NewFakeClock()

	client := &Client{
		config:                    Config{Topics: []string{"sr90/tdc/0"}, QoS: 0},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               fakeClock,
	}

	done := make(chan error, 1)
	go func() {
		done <- client.Connect()
	}()

	select {
	case err := <-done:
		t.Fatalf("Connect finished before subscription error injected: %v", err)
	default:
	}

	client.initialSubscriptionResult <- errors.New("subscribe failure")
	close(client.initialSubscriptionResult)

	if err := testutil.WaitForError(t, done, "Connect to propagate subscription error"); err == nil || !strings.Contains(err.Error(), "subscribe failure") {
		t.Fatalf("expected propagated error, got %v", err)
	}
}

func TestHandleConnectSubscribeFailureNotifies(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{
		subscribeFn: func(string, byte, paho.MessageHandler) paho.Token {
			return &stubToken{
				waitTimeoutResult: true,
				err:               errors.New("subscribe boom"),
			}
		},
		isOpen: true,
	}

	client := &Client{
		config:                    Config{Topics: []string{"sr90/tdc/0"}, QoS: 0},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               clock.RealClock{},
	}

	client.handleConnect(stub)

	if err := testutil.WaitForError(t, client.initialSubscriptionResult, "subscription error to propagate"); err == nil || err.Error() == "" {
		t.Fatal("expected subscription error to be propagated")
	}

	if stub.subscribeCalls != 1 {
		t.Fatalf("expected one subscribe attempt, got %d", stub.subscribeCalls)
	}
}

func TestHandleConnectIncrementsReconnectAttempts(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{
		isOpen: true,
	}
	stub.subscribeFn = func(string, byte, paho.MessageHandler) paho.Token {
		return &stubToken{waitTimeoutResult: true}
	}

	client := &Client{
		config:                    Config{Topics: []string{"sr90/tdc/0"}, QoS: 1},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               clock.RealClock{},
	}

	client.handleConnect(stub)
	// initialSubscriptionResult should have been closed with nil
	if err := testutil.WaitForError(t, client.initialSubscriptionResult, "first subscription result"); err != nil {
		t.Fatalf("expected first subscription success, got %v", err)
	}

	client.handleConnect(stub)

	if got := stub.subscribeCalls; got != 2 {
		t.Fatalf("expected subscribe called twice, got %d", got)
	}
	if client.connectAttempts != 2 {
		t.Fatalf("expected connectAttempts to be 2, got %d", client.connectAttempts)
	}
}

func TestSubscribeTimeoutAndError(t *testing.T) {
	t.Parallel()

	client := &Client{config: Config{Topics: []string{"topic/1"}, QoS: 1}}

	// Timeout case
	timeoutStub := &stubPahoClient{
		subscribeFn: func(string, byte, paho.MessageHandler) paho.Token {
			return &stubToken{waitTimeoutResult: false}
		},
	}
	if err := client.subscribe(timeoutStub); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}

	// Token error case
	errorStub := &stubPahoClient{
		subscribeFn: func(string, byte, paho.MessageHandler) paho.Token {
			return &stubToken{waitTimeoutResult: true, err: errors.New("bad token")}
		},
	}
	if err := client.subscribe(errorStub); err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("expected token error, got %v", err)
	}
}

func TestClient_CloseIsIdempotentAndDisconnects(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{isOpen: true}
	client := &Client{
		config:     Config{Topics: []string{"tdc/0"}},
		pahoClient: stub,
	}

	client.Close()
	client.Close()

	if stub.disconnectCalls != 1 {
		t.Fatalf("expected Disconnect invoked once, got %d", stub.disconnectCalls)
	}

	if stub.isOpen {
		t.Fatalf("expected connection to be closed after Disconnect")
	}
}

func TestNewClient_RequiredFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      Config
		errorSubstr string
	}{
		{
			name:        "missing broker",
			config:      Config{Topics: []string{"tdc/#"}},
			errorSubstr: "BrokerURL",
		},
		{
			name:        "missing topic",
			config:      Config{BrokerURL: "tcp://localhost:1883"},
			errorSubstr: "Topic",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewClient(tc.config, nil)
			if err == nil || !strings.Contains(err.Error(), tc.errorSubstr) {
				t.Fatalf("expected error containing %q, got %v (client=%v)", tc.errorSubstr, err, client)
			}
		})
	}
}

func TestGenerateClientIDUniquenessAndFormat(t *testing.T) {
	t.Parallel()

	ids := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id, err := generateClientID()
		if err != nil {
			t.Fatalf("generateClientID failed: %v", err)
		}
		if !strings.HasPrefix(id, "entropy-rx-") {
			t.Fatalf("expected prefix 'entropy-rx-', got %q", id)
		}
		suffix := strings.TrimPrefix(id, "entropy-rx-")
		if len(suffix) != 36 {
			t.Fatalf("expected 36-character UUID suffix, got %q", suffix)
		}
		if strings.Count(suffix, "-") != 4 {
			t.Fatalf("expected 4 hyphens in suffix, got %q", suffix)
		}
		compressed := strings.ReplaceAll(suffix, "-", "")
		if compressed == strings.Repeat("0", len(compressed)) {
			t.Fatalf("generated ID should not be all zeros: %q", id)
		}
		if _, exists := ids[id]; exists {
			t.Fatalf("duplicate client ID generated: %q", id)
		}
		ids[id] = struct{}{}
	}
}

func TestNewClientUsesProvidedClientID(t *testing.T) {
	t.Parallel()

	customID := "fixed-client-id"
	client, err := NewClient(Config{
		BrokerURL: "tcp://localhost:1883",
		ClientID:  customID,
		Topics:    []string{"sr90/tdc/#"},
		QoS:       0,
	}, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client.config.ClientID != customID {
		t.Fatalf("expected client ID %q, got %q", customID, client.config.ClientID)
	}
}

func TestNewClient_QoSClampedToOne(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{
		BrokerURL: "tcp://localhost:1883",
		Topics:    []string{"tdc/#"},
		QoS:       2,
	}, nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if client.config.QoS != 1 {
		t.Fatalf("expected QoS clamped to 1, got %d", client.config.QoS)
	}
}

func TestClient_ConnectTimeout(t *testing.T) {
	stub := &stubPahoClient{connectToken: &stubToken{waitTimeoutResult: false}}
	c := &Client{config: Config{Topics: []string{"t"}, QoS: 0}, pahoClient: stub, initialSubscriptionResult: make(chan error, 1)}
	err := c.Connect()
	if err == nil || !strings.Contains(err.Error(), "connect timeout") {
		t.Fatalf("got %v", err)
	}
}

func TestClient_ConnectTokenErrorAfterWait(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	// Token that waits successfully but returns an error
	stub := &stubPahoClient{
		connectToken: &stubToken{
			waitTimeoutResult: true,
			err:               errors.New("authentication failed"),
		},
	}

	fakeClock := clock.NewFakeClock()

	client := &Client{
		config: Config{
			BrokerURL: "tcp://dummy:1883",
			Topics:    []string{"sr90/tdc/0"},
			QoS:       0,
		},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               fakeClock,
	}

	err := client.Connect()

	// Verify error is returned
	if err == nil {
		t.Fatal("expected Connect to return error when token has error")
	}

	if !strings.Contains(err.Error(), "authentication failed") || !strings.Contains(err.Error(), "connect failed") {
		t.Errorf("expected error about authentication failure, got: %v", err)
	}

	// Verify NO subscription happened (since connect failed)
	if stub.subscribeCalls != 0 {
		t.Errorf("expected no subscribe call when connect fails, got %d", stub.subscribeCalls)
	}
}

func TestClient_HandleConnectSuccessSetsConnectedAndResult(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{
		subscribeFn: func(string, byte, paho.MessageHandler) paho.Token {
			return &stubToken{waitTimeoutResult: true} // Success
		},
		isOpen: true,
	}

	client := &Client{
		config:                    Config{Topics: []string{"sr90/tdc/0"}, QoS: 1},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               clock.RealClock{},
	}

	// Call handleConnect
	client.handleConnect(stub)

	// Verify initial subscription result is fulfilled with nil (success)
	select {
	case err, ok := <-client.initialSubscriptionResult:
		if !ok {
			// Channel closed, which means success
		} else if err != nil {
			t.Fatalf("expected initial subscription success (nil), got error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for initial subscription result")
	}

	// Verify connected metric was set to true (indirectly via no panic/log error)
	// Verify subscribe was called exactly once
	if stub.subscribeCalls != 1 {
		t.Errorf("expected subscribe called once, got %d", stub.subscribeCalls)
	}

	// Verify multiple calls don't panic (idempotency via sync.Once)
	client.handleConnect(stub)
	if stub.subscribeCalls != 2 {
		t.Errorf("expected subscribe called twice (once per handleConnect), got %d", stub.subscribeCalls)
	}
}

func TestClient_AfterDurationRealClockVsFakeClock(t *testing.T) {
	t.Parallel()

	t.Run("RealClock", func(t *testing.T) {
		t.Parallel()

		client := &Client{
			clockSource: nil, // No clock => RealClock fallback
		}

		start := time.Now()
		ch := client.afterDuration(10 * time.Millisecond)

		// Should receive after ~10ms
		select {
		case <-ch:
			elapsed := time.Since(start)
			if elapsed < 5*time.Millisecond {
				t.Errorf("afterDuration returned too quickly: %v", elapsed)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("afterDuration did not fire within 100ms")
		}
	})

	t.Run("FakeClock", func(t *testing.T) {
		t.Parallel()

		fakeClock := clock.NewFakeClock()

		client := &Client{
			clockSource: fakeClock,
		}

		ch := client.afterDuration(5 * time.Second)

		// Should NOT fire immediately
		select {
		case <-ch:
			t.Fatal("FakeClock timer fired before Fire() was called")
		case <-time.After(10 * time.Millisecond):
			// Expected: no fire yet
		}

		// Now fire the clock
		fakeClock.Fire()

		// Should receive now
		select {
		case ts := <-ch:
			if !ts.Equal(fakeClock.Now()) {
				t.Errorf("expected timer to fire at clock.Now()=%v, got %v", fakeClock.Now(), ts)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("FakeClock timer did not fire after Fire()")
		}
	})
}

type stubPahoClient struct {
	connectToken    paho.Token
	subscribeFn     func(string, byte, paho.MessageHandler) paho.Token
	subscribeCalls  int
	isOpen          bool
	disconnectCalls int
}

func (s *stubPahoClient) IsConnected() bool { return s.isOpen }

func (s *stubPahoClient) IsConnectionOpen() bool { return s.isOpen }

func (s *stubPahoClient) Connect() paho.Token {
	if s.connectToken != nil {
		return s.connectToken
	}
	return &stubToken{waitTimeoutResult: true}
}

func (s *stubPahoClient) Disconnect(uint) {
	s.disconnectCalls++
	s.isOpen = false
}

func (s *stubPahoClient) Publish(string, byte, bool, interface{}) paho.Token {
	return &stubToken{waitTimeoutResult: true}
}

func (s *stubPahoClient) Subscribe(topic string, qos byte, _ paho.MessageHandler) paho.Token {
	s.subscribeCalls++
	if s.subscribeFn != nil {
		return s.subscribeFn(topic, qos, nil)
	}
	return &stubToken{waitTimeoutResult: true}
}

func (s *stubPahoClient) SubscribeMultiple(map[string]byte, paho.MessageHandler) paho.Token {
	return &stubToken{waitTimeoutResult: true}
}

func (s *stubPahoClient) Unsubscribe(...string) paho.Token {
	return &stubToken{waitTimeoutResult: true}
}

func (s *stubPahoClient) AddRoute(string, paho.MessageHandler) {}

func (s *stubPahoClient) OptionsReader() paho.ClientOptionsReader {
	return paho.ClientOptionsReader{}
}

type stubToken struct {
	waitTimeoutResult bool
	err               error
}

func (t *stubToken) Wait() bool {
	return t.waitTimeoutResult
}

func (t *stubToken) WaitTimeout(time.Duration) bool {
	return t.waitTimeoutResult
}

func (t *stubToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (t *stubToken) Error() error {
	return t.err
}

func TestNewClient_TLSConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("TLS broker detection - ssl://", func(t *testing.T) {
		t.Parallel()

		// This will fail since there's no actual server, but we're testing TLS setup
		client, err := NewClient(Config{
			BrokerURL: "ssl://mqtt.example.com:8883",
			Topics:    []string{"test/topic"},
			QoS:       0,
		}, nil)
		// Should succeed in creating client (TLS config setup)
		if err != nil {
			t.Fatalf("NewClient with ssl:// failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
	})

	t.Run("TLS broker detection - tls://", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(Config{
			BrokerURL: "tls://mqtt.example.com:8883",
			Topics:    []string{"test/topic"},
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with tls:// failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
	})

	t.Run("TLS broker detection - mqtts://", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(Config{
			BrokerURL: "mqtts://mqtt.example.com:8883",
			Topics:    []string{"test/topic"},
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with mqtts:// failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
	})

	t.Run("TLS broker detection - tcps://", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(Config{
			BrokerURL: "tcps://mqtt.example.com:8883",
			Topics:    []string{"test/topic"},
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with tcps:// failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
	})

	t.Run("Non-TLS broker - tcp://", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(Config{
			BrokerURL: "tcp://localhost:1883",
			Topics:    []string{"test/topic"},
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with tcp:// failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
	})

	t.Run("TLS CA file not found", func(t *testing.T) {
		t.Parallel()

		_, err := NewClient(Config{
			BrokerURL: "ssl://mqtt.example.com:8883",
			Topics:    []string{"test/topic"},
			TLSCAFile: "/nonexistent/ca.crt",
			QoS:       0,
		}, nil)

		if err == nil {
			t.Fatal("expected error for missing CA file")
		}

		if !strings.Contains(err.Error(), "read CA certificate") {
			t.Errorf("expected CA certificate error, got: %v", err)
		}
	})

	t.Run("TLS CA file invalid PEM", func(t *testing.T) {
		t.Parallel()

		// Create a temp file with invalid PEM content
		tmpFile := t.TempDir() + "/invalid.crt"
		if err := os.WriteFile(tmpFile, []byte("not a valid PEM certificate"), 0o600); err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}

		_, err := NewClient(Config{
			BrokerURL: "ssl://mqtt.example.com:8883",
			Topics:    []string{"test/topic"},
			TLSCAFile: tmpFile,
			QoS:       0,
		}, nil)

		if err == nil {
			t.Fatal("expected error for invalid PEM")
		}

		if !strings.Contains(err.Error(), "failed to parse CA certificate") {
			t.Errorf("expected parse CA certificate error, got: %v", err)
		}
	})

	t.Run("TLS without CA file uses system pool", func(t *testing.T) {
		t.Parallel()

		// Create TLS client without specifying CA file
		// Should use system CA pool
		client, err := NewClient(Config{
			BrokerURL: "ssl://mqtt.example.com:8883",
			Topics:    []string{"test/topic"},
			QoS:       0,
			// TLSCAFile is empty, should use system CAs
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with system CA pool failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
	})
}

func TestNewClient_AuthenticationCredentials(t *testing.T) {
	t.Parallel()

	t.Run("Username and password set", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(Config{
			BrokerURL: "tcp://localhost:1883",
			Topics:    []string{"test/topic"},
			Username:  "testuser",
			Password:  "testpass",
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with auth failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
		if client.config.Username != "testuser" {
			t.Errorf("expected Username=testuser, got %s", client.config.Username)
		}
		if client.config.Password != "testpass" {
			t.Errorf("expected Password=testpass, got %s", client.config.Password)
		}
	})

	t.Run("Only username set", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(Config{
			BrokerURL: "tcp://localhost:1883",
			Topics:    []string{"test/topic"},
			Username:  "testuser",
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with username only failed: %v", err)
		}
		if client.config.Username != "testuser" {
			t.Errorf("expected Username=testuser, got %s", client.config.Username)
		}
	})

	t.Run("Only password set", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(Config{
			BrokerURL: "tcp://localhost:1883",
			Topics:    []string{"test/topic"},
			Password:  "testpass",
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient with password only failed: %v", err)
		}
		if client.config.Password != "testpass" {
			t.Errorf("expected Password=testpass, got %s", client.config.Password)
		}
	})
}

func TestClient_MessageHandlerWithNilHandler(t *testing.T) {
	t.Parallel()

	// Create client without a handler
	client, err := NewClient(Config{
		BrokerURL: "tcp://localhost:1883",
		Topics:    []string{"test/topic"},
		QoS:       0,
	}, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// The default publish handler should handle nil gracefully
	// We can't directly test the handler, but we verify client creation succeeds
	if client == nil {
		t.Fatal("expected client to be created")
	}
	if client.handler != nil {
		t.Error("expected handler to be nil")
	}
}

func TestClient_MessageHandlerWithHandler(t *testing.T) {
	t.Parallel()

	handlerCalled := false
	handler := &stubHandler{
		onMessage: func(topic string, payload []byte) {
			handlerCalled = true
		},
	}

	client, err := NewClient(Config{
		BrokerURL: "tcp://localhost:1883",
		Topics:    []string{"test/topic"},
		QoS:       0,
	}, handler)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if client.handler == nil {
		t.Fatal("expected handler to be set")
	}

	// Verify handler is stored
	client.handler.OnMessage("test", []byte("data"))
	if !handlerCalled {
		t.Error("expected handler to be called")
	}
}

func TestClient_ConnectionLostHandler(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	t.Run("Connection lost with error", func(t *testing.T) {
		// We can't directly trigger the connection lost handler,
		// but we verify client creation succeeds and handler is set up
		client, err := NewClient(Config{
			BrokerURL: "tcp://localhost:1883",
			Topics:    []string{"test/topic"},
			QoS:       0,
		}, nil)
		if err != nil {
			t.Fatalf("NewClient failed: %v", err)
		}
		if client == nil {
			t.Fatal("expected client to be created")
		}
	})
}

// ============================================================================
// Connect with Nil Client Tests
// ============================================================================

func TestClient_ConnectWithNilPahoClient(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	client := &Client{
		config: Config{
			Topics: []string{"test/topic"},
			QoS:    0,
		},
		pahoClient:                nil, // Nil paho client
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               clock.RealClock{},
	}

	err := client.Connect()
	if err == nil {
		t.Fatal("expected error when paho client is nil")
	}

	if !strings.Contains(err.Error(), "client not initialized") {
		t.Errorf("expected 'client not initialized' error, got: %v", err)
	}
}

// ============================================================================
// Initial Subscription Timeout Tests
// ============================================================================

func TestClient_InitialSubscriptionTimeout(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{
		connectToken: &stubToken{waitTimeoutResult: true},
	}

	fakeClock := clock.NewFakeClock()

	client := &Client{
		config: Config{
			Topics: []string{"sr90/tdc/0"},
			QoS:    0,
		},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               fakeClock,
	}

	done := make(chan error, 1)
	go func() {
		done <- client.Connect()
	}()

	// Don't send subscription result, let it timeout
	// Fire the clock to trigger timeout
	fakeClock.Fire()

	err := testutil.WaitForError(t, done, "Connect to timeout")
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !strings.Contains(err.Error(), "initial subscribe timeout") {
		t.Errorf("expected initial subscribe timeout error, got: %v", err)
	}
}

// ============================================================================
// Subscribe Timeout on Reconnect Tests
// ============================================================================

func TestHandleConnect_SubscribeTimeoutOnReconnect(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{
		subscribeFn: func(string, byte, paho.MessageHandler) paho.Token {
			return &stubToken{
				waitTimeoutResult: false, // Timeout
			}
		},
		isOpen: true,
	}

	client := &Client{
		config:                    Config{Topics: []string{"sr90/tdc/0"}, QoS: 0},
		pahoClient:                stub,
		initialSubscriptionResult: make(chan error, 1),
		clockSource:               clock.RealClock{},
	}

	client.handleConnect(stub)

	err := testutil.WaitForError(t, client.initialSubscriptionResult, "subscription timeout")
	if err == nil {
		t.Fatal("expected subscription timeout error")
	}

	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

// ============================================================================
// Close with Nil Client Tests
// ============================================================================

func TestClient_CloseWithNilPahoClient(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	client := &Client{
		config:     Config{Topics: []string{"test/topic"}},
		pahoClient: nil, // Nil paho client
	}

	// Should not panic
	client.Close()
}

func TestClient_CloseWithClosedConnection(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	stub := &stubPahoClient{isOpen: false} // Already closed
	client := &Client{
		config:     Config{Topics: []string{"test/topic"}},
		pahoClient: stub,
	}

	client.Close()

	// Should not call Disconnect since connection is already closed
	if stub.disconnectCalls != 0 {
		t.Errorf("expected no Disconnect calls, got %d", stub.disconnectCalls)
	}
}

// ============================================================================
// Generate Client ID Error Path Tests
// ============================================================================

func TestGenerateClientID_FormatValidation(t *testing.T) {
	t.Parallel()

	// Test UUIDv4 format compliance
	id, err := generateClientID()
	if err != nil {
		t.Fatalf("generateClientID failed: %v", err)
	}

	// Check version bits (should be 0x4XXX in the 3rd group)
	parts := strings.Split(strings.TrimPrefix(id, "entropy-rx-"), "-")
	if len(parts) != 5 {
		t.Fatalf("expected 5 UUID parts, got %d", len(parts))
	}

	// Version should be 4
	thirdGroup := parts[2]
	if !strings.HasPrefix(thirdGroup, "4") {
		t.Errorf("expected version 4 UUID (third group starts with 4), got: %s", thirdGroup)
	}

	// Variant bits (should be 10xx in the 4th group - means 8, 9, a, or b)
	fourthGroup := parts[3]
	firstChar := fourthGroup[0]
	if firstChar != '8' && firstChar != '9' && firstChar != 'a' && firstChar != 'b' {
		t.Errorf("expected variant 10 UUID (fourth group starts with 8/9/a/b), got: %c", firstChar)
	}
}

// ============================================================================
// QoS Boundary Tests
// ============================================================================

func TestNewClient_QoSBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		inputQoS byte
		wantQoS  byte
	}{
		{"QoS 0", 0, 0},
		{"QoS 1", 1, 1},
		{"QoS 2 clamped to 1", 2, 1},
		{"QoS 255 clamped to 1", 255, 1},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewClient(Config{
				BrokerURL: "tcp://localhost:1883",
				Topics:    []string{"test/topic"},
				QoS:       tc.inputQoS,
			}, nil)
			if err != nil {
				t.Fatalf("NewClient failed: %v", err)
			}

			if client.config.QoS != tc.wantQoS {
				t.Errorf("expected QoS=%d, got %d", tc.wantQoS, client.config.QoS)
			}
		})
	}
}

// ============================================================================
// TLS CA Certificate Tests
// ============================================================================

func TestCreateMQTTTLSConfig_CustomCA(t *testing.T) {
	t.Parallel()

	t.Run("missing CA file", func(t *testing.T) {
		cfg := Config{TLSCAFile: "/nonexistent/ca.pem"}
		_, err := createMQTTTLSConfig(cfg)
		if err == nil {
			t.Fatal("expected error for missing CA file")
		}
		if !strings.Contains(err.Error(), "read CA certificate") {
			t.Errorf("expected 'read CA certificate' error, got: %v", err)
		}
	})

	t.Run("invalid CA cert format", func(t *testing.T) {
		tmpDir := t.TempDir()
		caFile := tmpDir + "/invalid.pem"

		if err := os.WriteFile(caFile, []byte("not a valid cert"), 0o600); err != nil {
			t.Fatalf("failed to write invalid CA file: %v", err)
		}

		cfg := Config{TLSCAFile: caFile}
		_, err := createMQTTTLSConfig(cfg)
		if err == nil {
			t.Fatal("expected error for invalid CA cert")
		}
		if !strings.Contains(err.Error(), "failed to parse CA certificate") {
			t.Errorf("expected parse error, got: %v", err)
		}
	})

	t.Run("empty CA file path uses system pool", func(t *testing.T) {
		cfg := Config{TLSCAFile: ""}
		tlsConfig, err := createMQTTTLSConfig(cfg)
		if err != nil {
			t.Fatalf("createMQTTTLSConfig with empty CA failed: %v", err)
		}

		if tlsConfig == nil {
			t.Fatal("expected non-nil TLS config")
		}
		// RootCAs will be set to system pool (not nil)
		if tlsConfig.RootCAs == nil {
			t.Fatal("expected RootCAs to be set to system pool")
		}
	})
}

func TestGenerateClientID_ReturnsNonEmpty(t *testing.T) {
	t.Parallel()

	clientID, err := generateClientID()
	if err != nil {
		t.Fatalf("generateClientID returned error: %v", err)
	}
	if clientID == "" {
		t.Fatal("expected non-empty client ID")
	}
	if !strings.HasPrefix(clientID, "entropy-rx-") {
		t.Errorf("expected client ID to have prefix 'entropy-rx-', got %q", clientID)
	}
}

// ============================================================================
// Helper Types
// ============================================================================

type stubHandler struct {
	onMessage func(string, []byte)
}

func (h *stubHandler) OnMessage(topic string, payload []byte) {
	if h.onMessage != nil {
		h.onMessage(topic, payload)
	}
}
