package incident

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type delivery struct {
	body                                []byte
	completed, dead, abandoned, renewed atomic.Int32
	loseLock                            bool
	failComplete                        bool
}

func (d *delivery) Body() []byte { return d.body }
func (d *delivery) Renew(context.Context) error {
	d.renewed.Add(1)
	if d.loseLock {
		return errors.New("expired lock")
	}
	return nil
}
func (d *delivery) Complete(context.Context) error {
	if d.failComplete {
		return errors.New("settlement offline")
	}
	d.completed.Add(1)
	return nil
}
func (d *delivery) DeadLetter(context.Context, string) error { d.dead.Add(1); return nil }
func (d *delivery) Abandon(context.Context) error            { d.abandoned.Add(1); return nil }

type slowInbox struct {
	s     *Store
	delay time.Duration
}

func (i slowInbox) Accept(b []byte, c Config) error { time.Sleep(i.delay); return i.s.Accept(b, c) }

func TestLockRenewalExpiryAndRedelivery(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "renew", true: "expired"}[lost], func(t *testing.T) {
			c := testConfig(t)
			s := NewStore(c.StateDir)
			d := &delivery{body: alertBody("a", "Fired"), loseLock: lost}
			err := Handle(context.Background(), d, slowInbox{s, 30 * time.Millisecond}, c, time.Millisecond)
			if lost && (err == nil || !strings.Contains(err.Error(), "expired lock")) {
				t.Fatalf("lost lock error: %v", err)
			}
			if !lost && err != nil {
				t.Fatal(err)
			}
			if d.renewed.Load() == 0 || lost && d.completed.Load() != 0 {
				t.Fatal("lock handling")
			}
			retry := &delivery{body: d.body}
			if err := Handle(context.Background(), retry, s, c, time.Second); err != nil {
				t.Fatal(err)
			}
			rows, _ := s.List()
			if len(rows) != 1 || retry.completed.Load() != 1 {
				t.Fatalf("redelivery duplicated incident: %+v", rows)
			}
		})
	}
}

func TestDeadLettersAndSettlementFailure(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	poison := &delivery{body: []byte(`{"schemaId":"bad"}`)}
	if err := Handle(context.Background(), poison, s, c, time.Second); err != nil {
		t.Fatal(err)
	}
	if poison.dead.Load() != 1 || poison.completed.Load() != 0 {
		t.Fatal("poison not deadlettered")
	}
	d := &delivery{body: alertBody("a", "Fired"), failComplete: true}
	if err := Handle(context.Background(), d, s, c, time.Second); err == nil || !strings.Contains(err.Error(), "settlement offline") {
		t.Fatalf("settlement: %v", err)
	}
	rows, _ := s.List()
	if len(rows) != 1 {
		t.Fatal("settlement error lost durable alert")
	}
	broken := NewStore("relative-path")
	d = &delivery{body: alertBody("b", "Fired")}
	if err := Handle(context.Background(), d, broken, c, time.Second); err == nil {
		t.Fatal("storage failure hidden")
	}
	if d.abandoned.Load() != 1 || d.dead.Load() != 0 {
		t.Fatal("storage failure deadlettered valid alert")
	}
}

type offlineReceiver struct{}

func (offlineReceiver) Receive(ctx context.Context) ([]Delivery, error) {
	return nil, errors.New("offline")
}
func TestOfflineBacklogRecovery(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	ingest(t, s, "offline-backlog", "Fired", c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerDone := make(chan struct{})
	go func() { Consume(ctx, offlineReceiver{}, s, c); close(consumerDone) }()
	w := NewWorker(c, s, &testJobs{})
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	w.Wait()
	rows, _ := s.List()
	if rows[0].Status != "draft" {
		t.Fatalf("offline broker blocked backlog %+v", rows)
	}
	cancel()
	select {
	case <-consumerDone:
	case <-time.After(time.Second):
		t.Fatal("consumer did not stop")
	}
}
