package queue

import (
	"context"
	"errors"
	"net"
	"time"

	"ai-library/backend/internal/ports"
	amqp "github.com/rabbitmq/amqp091-go"
)

const ResultQueue = "index.results.v1"

var ErrConsume = errors.New("result consumer connection or delivery failed")

type Results struct {
	URL       string
	queueName string
}

func NewResults(url, name string) *Results { return &Results{URL: url, queueName: name} }

// Run consumes one connection session. The process retries failed sessions with
// backoff. Poison messages go to a durable dead-letter queue; transient failures
// requeue and end the session to avoid spinning on an unavailable dependency.
func (r *Results) Run(ctx context.Context, handle func(context.Context, ports.IndexResult) error) error {
	var socket net.Conn
	var stop func() bool
	conn, err := amqp.DialConfig(r.URL, amqp.Config{Heartbeat: 5 * time.Second, Dial: func(network, address string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var err error
		socket, err = (&net.Dialer{}).DialContext(dialCtx, network, address)
		if err == nil {
			_ = socket.SetDeadline(time.Now().Add(5 * time.Second))
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
		return ErrConsume
	}
	defer conn.Close()
	_ = socket.SetDeadline(time.Time{})
	ch, err := conn.Channel()
	if err != nil {
		return ErrConsume
	}
	defer ch.Close()
	name := r.queueName
	if name == "" {
		name = ResultQueue
	}
	if _, err = ch.QueueDeclare(name+".dead", true, false, false, false, nil); err != nil {
		return ErrConsume
	}
	if _, err = ch.QueueDeclare(name, true, false, false, false, amqp.Table{"x-dead-letter-exchange": "", "x-dead-letter-routing-key": name + ".dead"}); err != nil {
		return ErrConsume
	}
	if err = ch.Qos(1, 0, false); err != nil {
		return ErrConsume
	}
	deliveries, err := ch.Consume(name, "", false, false, false, false, nil)
	if err != nil {
		return ErrConsume
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return ErrConsume
			}
			result, err := ports.DecodeIndexResult(delivery.Body)
			if err == nil {
				err = handle(ctx, result)
			}
			if errors.Is(err, ports.ErrInvalidResult) {
				if delivery.Reject(false) != nil {
					return ErrConsume
				}
				continue
			}
			if err != nil {
				_ = delivery.Nack(false, true)
				return ErrConsume
			}
			if delivery.Ack(false) != nil {
				return ErrConsume
			}
		}
	}
}
