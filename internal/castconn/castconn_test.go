package castconn

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	pb "github.com/vishen/go-chromecast/cast/proto"
)

func newTLSListener(t *testing.T) net.Listener {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func writeFrame(t *testing.T, w io.Writer, namespace, payload string) {
	t.Helper()
	data, err := proto.Marshal(&pb.CastMessage{
		ProtocolVersion: pb.CastMessage_CASTV2_1_0.Enum(),
		SourceId:        new(ReceiverID),
		DestinationId:   new(SenderID),
		Namespace:       new(namespace),
		PayloadType:     pb.CastMessage_STRING.Enum(),
		PayloadUtf8:     new(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(data))); err != nil { //nolint:gosec // test frames are tiny.
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
}

func readFrame(t *testing.T, r io.Reader) (namespace string, payload map[string]any) {
	t.Helper()
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	var msg pb.CastMessage
	if err := proto.Unmarshal(buf, &msg); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(msg.GetPayloadUtf8()), &payload); err != nil {
		t.Fatal(err)
	}
	return msg.GetNamespace(), payload
}

func TestConnExchangesMessagesAndAnswersPings(t *testing.T) {
	ln := newTLSListener(t)
	serverConn := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil && c.(*tls.Conn).Handshake() == nil {
			serverConn <- c
		}
	}()

	conn, err := Dial(context.Background(), ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	srv := <-serverConn
	defer srv.Close()

	if err := conn.Send(ReceiverID, NamespaceReceiver, map[string]any{"type": "GET_STATUS"}); err != nil {
		t.Fatal(err)
	}
	ns, p := readFrame(t, srv)
	if ns != NamespaceReceiver || p["type"] != "GET_STATUS" || p["requestId"] == nil {
		t.Fatalf("unexpected request %s %v", ns, p)
	}

	writeFrame(t, srv, NamespaceHeartbeat, `{"type":"PING"}`)
	ns, p = readFrame(t, srv)
	if ns != NamespaceHeartbeat || p["type"] != "PONG" {
		t.Fatalf("expected PONG, got %s %v", ns, p)
	}

	writeFrame(t, srv, NamespaceMedia, `{"type":"MEDIA_STATUS","status":[]}`)
	select {
	case msg := <-conn.Messages():
		if msg.Namespace != NamespaceMedia {
			t.Fatalf("unexpected namespace %s", msg.Namespace)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message not delivered")
	}
}

func TestConnFailsWhenDeviceGoesSilent(t *testing.T) {
	ln := newTLSListener(t)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Complete the handshake and swallow keepalive PINGs without ever answering.
		_, _ = io.Copy(io.Discard, c)
	}()

	conn, err := Dial(context.Background(), ln.Addr().String(), 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	select {
	case <-conn.Done():
		if conn.Err() == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("silent connection was not detected")
	}
}
