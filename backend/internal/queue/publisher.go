package queue

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"ai-library/backend/internal/ports"
	amqp "github.com/rabbitmq/amqp091-go"
)

const TaskQueue = "index.tasks.v1"

var ErrPublish = errors.New("index task publish was not confirmed")

// A fresh connection for each delivery deliberately trades throughput for simple
// reconnect semantics during migration. No connection failure poisons later calls.
type Publisher struct {
	URL       string
	queueName string
}

var _ ports.Publisher = (*Publisher)(nil)

// Explicit queue selection is used for isolated migration deployments/tests.
// The legacy document_processing queue is never selected implicitly.
func NewPublisher(url, name string) *Publisher { return &Publisher{URL: url, queueName: name} }

func (p *Publisher) PublishConfirmed(ctx context.Context, task ports.IndexTask) error {
	name := p.queueName
	if name == "" {
		name = TaskQueue
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var socket net.Conn
	var stop func() bool
	conn, err := amqp.DialConfig(p.URL, amqp.Config{Heartbeat: time.Second, Dial: func(network, address string) (net.Conn, error) {
		var err error
		socket, err = (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil {
			deadline, _ := ctx.Deadline()
			_ = socket.SetDeadline(deadline)
			stop = context.AfterFunc(ctx, func() { _ = socket.Close() })
		}
		return socket, err
	}})
	if stop != nil {
		defer stop()
	}
	if socket != nil {
		defer socket.Close()
	}
	if err != nil {
		return ErrPublish
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return ErrPublish
	}
	defer channel.Close()
	if _, err = channel.QueueDeclare(name, true, false, false, false, nil); err != nil {
		return ErrPublish
	}
	if err = channel.Confirm(false); err != nil {
		return ErrPublish
	}
	returns := channel.NotifyReturn(make(chan amqp.Return, 1))
	body, err := json.Marshal(task)
	if err != nil {
		return ErrPublish
	}
	confirmation, err := channel.PublishWithDeferredConfirm("", name, true, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: task.TaskID, CorrelationId: task.TraceID, Body: body,
	})
	if err != nil || confirmation == nil {
		return ErrPublish
	}
	ack, err := confirmation.WaitContext(ctx)
	if err != nil || !ack {
		return ErrPublish
	}
	select {
	case <-returns:
		return ErrPublish
	default:
		return nil
	}
}
