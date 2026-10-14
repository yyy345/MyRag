package queue

import (
	"context"
	amqp "github.com/rabbitmq/amqp091-go"
	"net"
	"time"
)

// AMQP handshake only: no task publishing, queue mutation or model calls.
func Ping(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var socket net.Conn
	var stop func() bool
	conn, err := amqp.DialConfig(url, amqp.Config{Heartbeat: time.Second, Dial: func(network, address string) (net.Conn, error) {
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
	return nil
}
