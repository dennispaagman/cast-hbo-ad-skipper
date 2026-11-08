// Package castconn is a minimal Cast v2 protocol connection. It replaces go-chromecast's
// transport, which has no read/write deadlines and silently stops reading when the socket dies.
package castconn

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gogo/protobuf/proto"
	pb "github.com/vishen/go-chromecast/cast/proto"
)

const (
	NamespaceConnection = "urn:x-cast:com.google.cast.tp.connection"
	NamespaceHeartbeat  = "urn:x-cast:com.google.cast.tp.heartbeat"
	NamespaceReceiver   = "urn:x-cast:com.google.cast.receiver"
	NamespaceMedia      = "urn:x-cast:com.google.cast.media"

	SenderID   = "sender-0"
	ReceiverID = "receiver-0"

	maxFrameSize = 1 << 20
)

type Message struct {
	Namespace string
	Payload   []byte
}

// Conn is safe for concurrent Send calls. Done is closed once the connection fails or is closed,
// after which Err reports why.
type Conn struct {
	conn     net.Conn
	timeout  time.Duration
	writeMu  sync.Mutex
	messages chan Message
	done     chan struct{}
	failOnce sync.Once
	err      error
	reqID    atomic.Int64
}

// Dial connects to a Cast device. timeout bounds the dial and every read and write; the
// connection sends keepalive PINGs often enough that an idle but healthy device never hits it.
func Dial(ctx context.Context, addr string, timeout time.Duration) (*Conn, error) {
	dialer := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Cast devices use self-signed certificates.
	}
	nc, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Conn{
		conn:     nc,
		timeout:  timeout,
		messages: make(chan Message, 32),
		done:     make(chan struct{}),
	}
	go c.readLoop()
	go c.keepalive()
	return c, nil
}

func (c *Conn) Messages() <-chan Message { return c.messages }
func (c *Conn) Done() <-chan struct{}    { return c.done }

func (c *Conn) Err() error {
	<-c.done
	return c.err
}

func (c *Conn) Close() error {
	c.fail(net.ErrClosed)
	return nil
}

// Send writes a JSON message. Requests on the receiver and media namespaces get a requestId.
func (c *Conn) Send(destinationID, namespace string, payload map[string]any) error {
	if namespace == NamespaceReceiver || namespace == NamespaceMedia {
		payload["requestId"] = c.reqID.Add(1)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data, err := proto.Marshal(&pb.CastMessage{
		ProtocolVersion: pb.CastMessage_CASTV2_1_0.Enum(),
		SourceId:        new(SenderID),
		DestinationId:   new(destinationID),
		Namespace:       new(namespace),
		PayloadType:     pb.CastMessage_STRING.Enum(),
		PayloadUtf8:     new(string(body)),
	})
	if err != nil {
		return err
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(data)), uint32(len(data))) //nolint:gosec // proto messages are far below 4GB.
	frame = append(frame, data...)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		c.fail(err)
		return err
	}
	if _, err := c.conn.Write(frame); err != nil {
		c.fail(err)
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func (c *Conn) readLoop() {
	var header [4]byte
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			c.fail(err)
			return
		}
		if _, err := io.ReadFull(c.conn, header[:]); err != nil {
			c.fail(fmt.Errorf("read: %w", err))
			return
		}
		size := binary.BigEndian.Uint32(header[:])
		if size > maxFrameSize {
			c.fail(fmt.Errorf("frame of %d bytes exceeds limit", size))
			return
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(c.conn, buf); err != nil {
			c.fail(fmt.Errorf("read: %w", err))
			return
		}
		var msg pb.CastMessage
		if err := proto.Unmarshal(buf, &msg); err != nil {
			c.fail(fmt.Errorf("decode frame: %w", err))
			return
		}

		payload := []byte(msg.GetPayloadUtf8())
		if msg.GetNamespace() == NamespaceHeartbeat {
			var p struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(payload, &p) == nil && p.Type == "PING" {
				_ = c.Send(msg.GetSourceId(), NamespaceHeartbeat, map[string]any{"type": "PONG"})
			}
			continue
		}

		select {
		case c.messages <- Message{Namespace: msg.GetNamespace(), Payload: payload}:
		case <-c.done:
			return
		}
	}
}

func (c *Conn) keepalive() {
	ticker := time.NewTicker(c.timeout / 3)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := c.Send(ReceiverID, NamespaceHeartbeat, map[string]any{"type": "PING"}); err != nil {
				return
			}
		}
	}
}

func (c *Conn) fail(err error) {
	c.failOnce.Do(func() {
		if errors.Is(err, io.EOF) {
			err = fmt.Errorf("device closed the connection: %w", err)
		}
		c.err = err
		close(c.done)
		_ = c.conn.Close()
	})
}
