package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"ai-library/backend/internal/ports"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestPublisherIntegration(t *testing.T) {
	if os.Getenv("RUN_RABBITMQ_TESTS") != "1" {
		t.Skip("requires isolated RabbitMQ")
	}
	const broker = "amqp://guest:guest@127.0.0.1:55673/"
	conn, err := amqp.Dial(broker)
	if err != nil {
		t.Fatal("local broker unavailable", err)
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	name := fmt.Sprintf("migration.index.test.%d", time.Now().UnixNano())
	defer channel.QueueDelete(name, false, false, false)
	publisher := &Publisher{URL: broker, queueName: name}
	task := ports.IndexTask{SchemaVersion: 1, TaskID: "11111111-1111-4111-8111-111111111111", EntryID: "22222222-2222-4222-8222-222222222222", KBID: "33333333-3333-4333-8333-333333333333", EntryVersion: 1, TraceID: "local-only"}
	for range 2 {
		if err = publisher.PublishConfirmed(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		message, ok, err := channel.Get(name, false)
		if err != nil || !ok {
			t.Fatal("confirmed message missing", err)
		}
		var got ports.IndexTask
		if json.Unmarshal(message.Body, &got) != nil || got != task || message.MessageId != task.TaskID || message.DeliveryMode != amqp.Persistent {
			t.Fatal("message contract mismatch")
		}
		if err = message.Ack(false); err != nil {
			t.Fatal(err)
		}
	}
	// A transport that disconnects during AMQP handshake must not claim success.
	broken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	go func() {
		socket, err := broken.Accept()
		if err == nil {
			socket.Close()
		}
	}()
	publisher.URL = "amqp://guest:guest@" + broken.Addr().String() + "/"
	if err = publisher.PublishConfirmed(context.Background(), task); err == nil {
		t.Fatal("disconnected broker accepted")
	}
	publisher.URL = broker
	if err = publisher.PublishConfirmed(context.Background(), task); err != nil {
		t.Fatal("publish did not recover", err)
	}
	message, ok, err := channel.Get(name, false)
	if err != nil || !ok {
		t.Fatal("recovered message missing", err)
	}
	_ = message.Ack(false)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = publisher.PublishConfirmed(cancelled, task); err == nil {
		t.Fatal("cancelled publish accepted")
	}
}
