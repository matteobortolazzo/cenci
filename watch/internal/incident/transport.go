package incident

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
)

type Delivery interface {
	Body() []byte
	Renew(context.Context) error
	Complete(context.Context) error
	DeadLetter(context.Context, string) error
	Abandon(context.Context) error
}
type Receiver interface {
	Receive(context.Context) ([]Delivery, error)
}
type Inbox interface{ Accept([]byte, Config) error }
type Rejected struct{ Reason string }

func (r *Rejected) Error() string             { return r.Reason }
func reject(format string, args ...any) error { return &Rejected{fmt.Sprintf(format, args...)} }

// Handle renews the broker lock only through local durable intake, never through
// investigation. A failed settlement is safe: redelivery finds the saved identity.
func Handle(ctx context.Context, d Delivery, inbox Inbox, c Config, renewEvery time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	renewed := make(chan error, 1)
	go func() {
		t := time.NewTicker(renewEvery)
		defer t.Stop()
		for {
			select {
			case <-stopped:
				renewed <- nil
				return
			case <-ctx.Done():
				renewed <- ctx.Err()
				return
			case <-t.C:
				if err := d.Renew(ctx); err != nil {
					renewed <- err
					return
				}
			}
		}
	}()
	err := inbox.Accept(d.Body(), c)
	close(stopped)
	renewErr := <-renewed
	if renewErr != nil {
		return fmt.Errorf("broker lock renewal: %w", renewErr)
	}
	if err != nil {
		var rejected *Rejected
		if errors.As(err, &rejected) {
			return d.DeadLetter(ctx, "Invalid or unallowlisted common alert")
		}
		abandonErr := d.Abandon(ctx)
		return errors.Join(err, abandonErr)
	}
	return d.Complete(ctx)
}

func Consume(ctx context.Context, r Receiver, inbox Inbox, c Config) {
	delay := time.Second
	for ctx.Err() == nil {
		receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		messages, err := r.Receive(receiveCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				logError("incident receive failed; durable backlog continues", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < 30*time.Second {
				delay *= 2
			}
			continue
		}
		delay = time.Second
		for _, m := range messages {
			if err := Handle(ctx, m, inbox, c, 5*time.Second); err != nil {
				logError("incident intake/settlement failed; delivery may retry", err)
			}
		}
	}
}

type AzureReceiver struct {
	client   *azservicebus.Client
	receiver *azservicebus.Receiver
}

// ServiceBusReceiver is the full method set of *azservicebus.Receiver the
// package touches. `cenci incident verify` sits behind it so a test can count
// every receive and settlement call and prove a peek made none.
type ServiceBusReceiver interface {
	ReceiveMessages(ctx context.Context, maxMessages int, options *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error)
	PeekMessages(ctx context.Context, maxMessageCount int, options *azservicebus.PeekMessagesOptions) ([]*azservicebus.ReceivedMessage, error)
	RenewMessageLock(ctx context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.RenewMessageLockOptions) error
	CompleteMessage(ctx context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.CompleteMessageOptions) error
	AbandonMessage(ctx context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.AbandonMessageOptions) error
	DeadLetterMessage(ctx context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.DeadLetterOptions) error
	Close(ctx context.Context) error
}

var _ ServiceBusReceiver = (*azservicebus.Receiver)(nil)

// newServiceBusClient builds the broker client. It is a variable only so a test
// can observe the options a production opener passes; building a client makes no
// network call.
var newServiceBusClient = azservicebus.NewClient

// receiverFactory is the part of *azservicebus.Client that picks the entity.
type receiverFactory interface {
	NewReceiverForQueue(queue string, options *azservicebus.ReceiverOptions) (*azservicebus.Receiver, error)
	NewReceiverForSubscription(topic, subscription string, options *azservicebus.ReceiverOptions) (*azservicebus.Receiver, error)
}

// receiverFor opens a receiver on the configured queue, or else on the topic
// subscription.
func receiverFor(f receiverFactory, c Config) (*azservicebus.Receiver, error) {
	if c.Queue != "" {
		return f.NewReceiverForQueue(c.Queue, nil)
	}
	return f.NewReceiverForSubscription(c.Topic, c.Subscription, nil)
}

// openReceiver opens a client and a receiver on the configured queue or topic
// subscription. On failure nothing stays open.
func openReceiver(c Config, credential azcore.TokenCredential, opts *azservicebus.ClientOptions) (*azservicebus.Client, *azservicebus.Receiver, error) {
	client, err := newServiceBusClient(c.Namespace, credential, opts)
	if err != nil {
		return nil, nil, err
	}
	r, err := receiverFor(client, c)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Close(ctx)
		return nil, nil, err
	}
	return client, r, nil
}

func NewAzureReceiver(c Config, credential azcore.TokenCredential) (*AzureReceiver, error) {
	client, r, err := openReceiver(c, credential, nil)
	if err != nil {
		return nil, err
	}
	return &AzureReceiver{client: client, receiver: r}, nil
}
func (a *AzureReceiver) Close(ctx context.Context) error {
	return errors.Join(a.receiver.Close(ctx), a.client.Close(ctx))
}
func (a *AzureReceiver) Receive(ctx context.Context) ([]Delivery, error) {
	ms, err := a.receiver.ReceiveMessages(ctx, 1, nil)
	out := make([]Delivery, 0, len(ms))
	for _, m := range ms {
		out = append(out, &azureDelivery{r: a.receiver, m: m})
	}
	return out, err
}

type azureDelivery struct {
	r *azservicebus.Receiver
	m *azservicebus.ReceivedMessage
}

func (d *azureDelivery) Body() []byte                    { return d.m.Body }
func (d *azureDelivery) Renew(ctx context.Context) error { return d.r.RenewMessageLock(ctx, d.m, nil) }
func (d *azureDelivery) Complete(ctx context.Context) error {
	return d.r.CompleteMessage(ctx, d.m, nil)
}
func (d *azureDelivery) Abandon(ctx context.Context) error { return d.r.AbandonMessage(ctx, d.m, nil) }
func (d *azureDelivery) DeadLetter(ctx context.Context, reason string) error {
	return d.r.DeadLetterMessage(ctx, d.m, &azservicebus.DeadLetterOptions{Reason: &reason})
}
