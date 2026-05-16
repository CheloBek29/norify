package runtime

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestNewRedisClientParsesURL(t *testing.T) {
	client, err := NewRedisClient("redis://:secret@redis.local:6380/2")
	if err != nil {
		t.Fatalf("NewRedisClient failed: %v", err)
	}
	if client.addr != "redis.local:6380" {
		t.Fatalf("addr = %q", client.addr)
	}
	if client.password != "secret" {
		t.Fatalf("password = %q", client.password)
	}
	if client.db != "2" {
		t.Fatalf("db = %q", client.db)
	}
}

func TestRedisClientCommands(t *testing.T) {
	server := newFakeRedis(map[string]string{"channel-config:email": `{"code":"email"}`})
	client, err := NewRedisClient("redis://127.0.0.1:6379/0")
	if err != nil {
		t.Fatalf("NewRedisClient failed: %v", err)
	}
	client.dial = server.dial

	value, ok, err := client.Get(context.Background(), "channel-config:email")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !ok || value != `{"code":"email"}` {
		t.Fatalf("Get = %q, %v", value, ok)
	}
	if err := client.SetEX(context.Background(), "channel-config:sms", time.Minute, `{"code":"sms"}`); err != nil {
		t.Fatalf("SetEX failed: %v", err)
	}
	if err := client.Del(context.Background(), "channel-config:email"); err != nil {
		t.Fatalf("Del failed: %v", err)
	}
	acquired, err := client.SetNXEX(context.Background(), "delivery-lock:key", 15*time.Second, "worker-1")
	if err != nil {
		t.Fatalf("SetNXEX failed: %v", err)
	}
	if !acquired {
		t.Fatalf("SetNXEX should acquire empty lock")
	}
	acquired, err = client.SetNXEX(context.Background(), "delivery-lock:key", 15*time.Second, "worker-2")
	if err != nil {
		t.Fatalf("SetNXEX duplicate failed: %v", err)
	}
	if acquired {
		t.Fatalf("SetNXEX should reject duplicate lock")
	}
	if err := client.DelIfValue(context.Background(), "delivery-lock:key", "wrong-worker"); err != nil {
		t.Fatalf("DelIfValue wrong owner failed: %v", err)
	}
	if server.values["delivery-lock:key"] != "worker-1" {
		t.Fatalf("DelIfValue deleted another owner")
	}
	if err := client.DelIfValue(context.Background(), "delivery-lock:key", "worker-1"); err != nil {
		t.Fatalf("DelIfValue owner failed: %v", err)
	}
	if _, exists := server.values["delivery-lock:key"]; exists {
		t.Fatalf("DelIfValue did not delete owner lock")
	}

	want := [][]string{
		{"GET", "channel-config:email"},
		{"SETEX", "channel-config:sms", "60", `{"code":"sms"}`},
		{"DEL", "channel-config:email"},
		{"SET", "delivery-lock:key", "worker-1", "NX", "EX", "15"},
		{"SET", "delivery-lock:key", "worker-2", "NX", "EX", "15"},
		{"EVAL", redisCompareAndDeleteScript, "1", "delivery-lock:key", "wrong-worker"},
		{"EVAL", redisCompareAndDeleteScript, "1", "delivery-lock:key", "worker-1"},
	}
	if !reflect.DeepEqual(server.commands(), want) {
		t.Fatalf("commands = %#v, want %#v", server.commands(), want)
	}
}

type fakeRedis struct {
	values map[string]string
	seen   chan []string
}

func newFakeRedis(values map[string]string) *fakeRedis {
	return &fakeRedis{
		values: values,
		seen:   make(chan []string, 10),
	}
}

func (s *fakeRedis) dial(_ context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	go s.handle(server)
	return client, nil
}

func (s *fakeRedis) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	args, err := readRedisCommand(reader)
	if err != nil {
		return
	}
	s.seen <- args
	switch args[0] {
	case "GET":
		value, ok := s.values[args[1]]
		if !ok {
			_, _ = conn.Write([]byte("$-1\r\n"))
			return
		}
		_, _ = fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(value), value)
	case "SETEX":
		s.values[args[1]] = args[3]
		_, _ = conn.Write([]byte("+OK\r\n"))
	case "DEL":
		delete(s.values, args[1])
		_, _ = conn.Write([]byte(":1\r\n"))
	case "SET":
		if len(args) >= 5 && args[3] == "NX" {
			if _, exists := s.values[args[1]]; exists {
				_, _ = conn.Write([]byte("$-1\r\n"))
				return
			}
		}
		s.values[args[1]] = args[2]
		_, _ = conn.Write([]byte("+OK\r\n"))
	case "EVAL":
		key := args[3]
		value := args[4]
		if s.values[key] == value {
			delete(s.values, key)
			_, _ = conn.Write([]byte(":1\r\n"))
			return
		}
		_, _ = conn.Write([]byte(":0\r\n"))
	default:
		_, _ = conn.Write([]byte("-ERR unsupported\r\n"))
	}
}

func (s *fakeRedis) commands() [][]string {
	out := [][]string{}
	for {
		select {
		case command := <-s.seen:
			out = append(out, command)
		default:
			return out
		}
	}
}

func readRedisCommand(reader *bufio.Reader) ([]string, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix != '*' {
		return nil, fmt.Errorf("unexpected prefix %q", prefix)
	}
	line, err := readRedisLine(reader)
	if err != nil {
		return nil, err
	}
	count := 0
	_, _ = fmt.Sscanf(line, "%d", &count)
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if b, err := reader.ReadByte(); err != nil || b != '$' {
			return nil, fmt.Errorf("bad bulk prefix")
		}
		sizeLine, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		size := 0
		_, _ = fmt.Sscanf(sizeLine, "%d", &size)
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func TestRedisRESPReadWrite(t *testing.T) {
	var buf bytes.Buffer
	if err := writeRedisCommand(&buf, "SETEX", "k", "60", "v"); err != nil {
		t.Fatalf("writeRedisCommand failed: %v", err)
	}
	if got := buf.String(); got != "*4\r\n$5\r\nSETEX\r\n$1\r\nk\r\n$2\r\n60\r\n$1\r\nv\r\n" {
		t.Fatalf("command = %q", got)
	}
	value, err := readRedisValue(bufio.NewReader(bytes.NewBufferString("$5\r\nvalue\r\n")))
	if err != nil {
		t.Fatalf("readRedisValue failed: %v", err)
	}
	if value != "value" {
		t.Fatalf("value = %#v", value)
	}
}
