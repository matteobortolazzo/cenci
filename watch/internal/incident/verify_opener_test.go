package incident

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
)

// clientCapture replaces the broker client constructor with one that records
// what the opener under test passed it and then builds the real client, which
// makes no network call until it is used.
type clientCapture struct {
	calls     int
	namespace string
	opts      *azservicebus.ClientOptions
}

func captureClientConstructor(t *testing.T) *clientCapture {
	t.Helper()
	got := &clientCapture{}
	real := newServiceBusClient
	newServiceBusClient = func(namespace string, cred azcore.TokenCredential, opts *azservicebus.ClientOptions) (*azservicebus.Client, error) {
		got.calls++
		got.namespace = namespace
		got.opts = opts
		return real(namespace, cred, opts)
	}
	t.Cleanup(func() { newServiceBusClient = real })
	return got
}

func TestVerifyProductionBrokerOpenerAttemptsOnceAtTheSDKLevel(t *testing.T) {
	cfg := validConfig(t)
	cred := &scopeCredential{}
	got := captureClientConstructor(t)

	receiver, err := openVerifyBroker(cfg, cred)
	if err != nil {
		t.Fatalf("openVerifyBroker: %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = receiver.Close(closeCtx) })

	if got.calls != 1 || got.namespace != cfg.Namespace {
		t.Fatalf("client built %d time(s) for namespace %q, want once for %q", got.calls, got.namespace, cfg.Namespace)
	}
	if got.opts == nil || got.opts.RetryOptions.MaxRetries >= 0 {
		t.Fatalf("verify broker client options = %+v, want a negative MaxRetries (one try)", got.opts)
	}
}

func TestRunBrokerOpenerKeepsTheSDKRetryDefaults(t *testing.T) {
	cfg := validConfig(t)
	got := captureClientConstructor(t)

	receiver, err := NewAzureReceiver(cfg, &scopeCredential{})
	if err != nil {
		t.Fatalf("NewAzureReceiver: %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = receiver.Close(closeCtx) })

	if got.opts != nil {
		t.Fatalf("run broker client options = %+v, want the SDK defaults (nil)", got.opts)
	}
}

// fakeReceiverFactory records which entity a receiver was requested on.
type fakeReceiverFactory struct {
	queues []string
	topics [][2]string
}

func (f *fakeReceiverFactory) NewReceiverForQueue(queue string, _ *azservicebus.ReceiverOptions) (*azservicebus.Receiver, error) {
	f.queues = append(f.queues, queue)
	return nil, nil
}

func (f *fakeReceiverFactory) NewReceiverForSubscription(topic, subscription string, _ *azservicebus.ReceiverOptions) (*azservicebus.Receiver, error) {
	f.topics = append(f.topics, [2]string{topic, subscription})
	return nil, nil
}

func TestBrokerReceiverIsOpenedOnTheConfiguredQueueOrTopicSubscription(t *testing.T) {
	t.Run("queue", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Queue, cfg.Topic, cfg.Subscription = "alerts-queue", "", ""
		f := &fakeReceiverFactory{}
		if _, err := receiverFor(f, cfg); err != nil {
			t.Fatal(err)
		}
		if len(f.queues) != 1 || f.queues[0] != "alerts-queue" || len(f.topics) != 0 {
			t.Fatalf("queue config opened queues=%v topics=%v, want only queue alerts-queue", f.queues, f.topics)
		}
	})
	t.Run("topic subscription", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.Queue, cfg.Topic, cfg.Subscription = "", "alerts-topic", "worker-sub"
		f := &fakeReceiverFactory{}
		if _, err := receiverFor(f, cfg); err != nil {
			t.Fatal(err)
		}
		if len(f.topics) != 1 || f.topics[0] != [2]string{"alerts-topic", "worker-sub"} || len(f.queues) != 0 {
			t.Fatalf("topic config opened queues=%v topics=%v, want only alerts-topic/worker-sub", f.queues, f.topics)
		}
	})
}
