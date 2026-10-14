package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"ai-library/backend/internal/ports"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestResultConsumerIntegration(t *testing.T) {
	if os.Getenv("RUN_RABBITMQ_TESTS") != "1" {
		t.Skip("requires isolated RabbitMQ")
	}
	const broker = "amqp://guest:guest@127.0.0.1:55673/"
	conn, err := amqp.Dial(broker)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	name := fmt.Sprintf("migration.results.test.%d", time.Now().UnixNano())
	for _, q := range []string{name + ".dead", name} {
		var args amqp.Table
		if q == name {
			args = amqp.Table{"x-dead-letter-exchange": "", "x-dead-letter-routing-key": name + ".dead"}
		}
		if _, err = ch.QueueDeclare(q, true, false, false, false, args); err != nil {
			t.Fatal(err)
		}
		defer ch.QueueDelete(q, false, false, false)
	}
	result := ports.IndexResult{SchemaVersion: 1, TaskID: "11111111-1111-4111-8111-111111111111", EntryID: "22222222-2222-4222-8222-222222222222", EntryVersion: 1, TraceID: "local", Status: "COMPLETED"}
	body, _ := json.Marshal(result)
	publish := func(body []byte) {
		t.Helper()
		if err := ch.Publish("", name, true, false, amqp.Publishing{DeliveryMode: amqp.Persistent, ContentType: "application/json", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	publish(body)
	consumer := &Results{URL: broker, queueName: name}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = consumer.Run(ctx, func(context.Context, ports.IndexResult) error { return errors.New("temporary database outage") })
	if !errors.Is(err, ErrConsume) {
		t.Fatal("transient error should end session for backoff", err)
	}
	handled := make(chan ports.IndexResult, 1)
	done := make(chan error, 1)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		done <- consumer.Run(runCtx, func(_ context.Context, v ports.IndexResult) error { handled <- v; return nil })
	}()
	select {
	case got := <-handled:
		if got.TaskID != result.TaskID {
			t.Fatal("wrong redelivery")
		}
	case <-ctx.Done():
		t.Fatal("failed result was not requeued")
	}
	// With prefetch 1, handling the trailing poison message proves the prior
	// successful delivery was ACKed before the poison message was dispatched.
	publish([]byte(`{"schema_version":999}`))
	for {
		message, ok, err := ch.Get(name+".dead", true)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			if string(message.Body) != `{"schema_version":999}` {
				t.Fatal("wrong dead letter")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("poison result not dead-lettered")
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("consumer did not stop")
	}
	if _, ok, err := ch.Get(name, true); err != nil || ok {
		t.Fatal("successful message was not acknowledged", err)
	}
}
