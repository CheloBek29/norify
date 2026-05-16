package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type RedisClient struct {
	addr     string
	password string
	db       string
	timeout  time.Duration
	dial     func(context.Context, string, string) (net.Conn, error)
}

func NewRedisClient(rawURL string) (*RedisClient, error) {
	if rawURL == "" {
		return nil, errors.New("REDIS_URL is empty")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "redis" {
		return nil, fmt.Errorf("unsupported redis scheme: %s", u.Scheme)
	}
	addr := u.Host
	if !strings.Contains(addr, ":") {
		addr += ":6379"
	}
	password, _ := u.User.Password()
	db := strings.TrimPrefix(u.Path, "/")
	return &RedisClient{addr: addr, password: password, db: db, timeout: 2 * time.Second}, nil
}

func NewRedisClientFromEnv() (*RedisClient, error) {
	return NewRedisClient(Env("REDIS_URL", ""))
}

func (c *RedisClient) Get(ctx context.Context, key string) (string, bool, error) {
	value, err := c.do(ctx, "GET", key)
	if errors.Is(err, errRedisNil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	text, ok := value.(string)
	return text, ok, nil
}

func (c *RedisClient) SetEX(ctx context.Context, key string, ttl time.Duration, value string) error {
	seconds := int(ttl.Seconds())
	if seconds <= 0 {
		seconds = 60
	}
	_, err := c.do(ctx, "SETEX", key, strconv.Itoa(seconds), value)
	return err
}

func (c *RedisClient) SetNXEX(ctx context.Context, key string, ttl time.Duration, value string) (bool, error) {
	seconds := int(ttl.Seconds())
	if seconds <= 0 {
		seconds = 15
	}
	reply, err := c.do(ctx, "SET", key, value, "NX", "EX", strconv.Itoa(seconds))
	if errors.Is(err, errRedisNil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return reply == "OK", nil
}

func (c *RedisClient) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	args := append([]string{"DEL"}, keys...)
	_, err := c.do(ctx, args...)
	return err
}

const redisCompareAndDeleteScript = "if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) else return 0 end"

func (c *RedisClient) DelIfValue(ctx context.Context, key, value string) error {
	_, err := c.do(ctx, "EVAL", redisCompareAndDeleteScript, "1", key, value)
	return err
}

func (c *RedisClient) do(ctx context.Context, args ...string) (any, error) {
	dial := c.dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: c.timeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", c.addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	reader := bufio.NewReader(conn)
	if c.password != "" {
		if err := writeRedisCommand(conn, "AUTH", c.password); err != nil {
			return nil, err
		}
		if _, err := readRedisValue(reader); err != nil {
			return nil, err
		}
	}
	if c.db != "" && c.db != "0" {
		if err := writeRedisCommand(conn, "SELECT", c.db); err != nil {
			return nil, err
		}
		if _, err := readRedisValue(reader); err != nil {
			return nil, err
		}
	}
	if err := writeRedisCommand(conn, args...); err != nil {
		return nil, err
	}
	return readRedisValue(reader)
}

func writeRedisCommand(w io.Writer, args ...string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

var errRedisNil = errors.New("redis nil")

func readRedisValue(reader *bufio.Reader) (any, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch prefix {
	case '+':
		line, err := readRedisLine(reader)
		return line, err
	case '-':
		line, _ := readRedisLine(reader)
		return nil, errors.New(line)
	case ':':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		return strconv.Atoi(line)
	case '$':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, errRedisNil
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:size]), nil
	default:
		return nil, fmt.Errorf("unsupported redis response prefix: %q", prefix)
	}
}

func readRedisLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
