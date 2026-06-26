package db

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
)

// StartMockRedisServer runs a simple RESP-compatible TCP server on the given port.
// It matches basic Redis commands and returns standard RESP responses.
func StartMockRedisServer(port string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return err
	}

	slog.Info("starting in-process mock Redis server...", "port", port)
	go func() {
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				slog.Debug("mock Redis server listener closed", "error", err)
				return
			}
			go handleRedisClient(conn)
		}
	}()
	return nil
}

func handleRedisClient(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		// Read RESP line (usually *<args>\r\n)
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "*") {
			// Not a valid RESP array start, reply with error
			_, _ = conn.Write([]byte("-ERR only array commands supported\r\n"))
			continue
		}

		var numArgs int
		_, err = fmt.Sscanf(line, "*%d\r\n", &numArgs)
		if err != nil {
			return
		}

		args := make([]string, numArgs)
		for i := 0; i < numArgs; i++ {
			// Read bulk string length ($<len>\r\n)
			lenLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			var strLen int
			_, err = fmt.Sscanf(lenLine, "$%d\r\n", &strLen)
			if err != nil {
				return
			}

			// Read actual string contents + trailing \r\n
			buf := make([]byte, strLen+2)
			_, err = io.ReadFull(reader, buf)
			if err != nil {
				return
			}
			args[i] = string(buf[:strLen])
		}

		if len(args) == 0 {
			continue
		}

		cmd := strings.ToUpper(args[0])
		var reply []byte

		switch cmd {
		case "PING":
			reply = []byte("+PONG\r\n")
		case "HELLO":
			reply = []byte("-ERR RESP3 not supported\r\n")
		case "XADD":
			// Returns generated entry ID
			reply = []byte("+1700000000000-0\r\n")
		case "XGROUP":
			reply = []byte("+OK\r\n")
		case "XREADGROUP":
			// Return empty array so go-redis is happy and blocks/waits
			reply = []byte("*0\r\n")
		case "XACK":
			reply = []byte(":1\r\n")
		case "ZREMRANGEBYSCORE":
			reply = []byte(":0\r\n")
		case "ZADD":
			reply = []byte(":1\r\n")
		case "EXPIRE":
			reply = []byte(":1\r\n")
		case "ZCARD":
			reply = []byte(":0\r\n")
		case "GET":
			reply = []byte("$-1\r\n")
		case "SET":
			reply = []byte("+OK\r\n")
		default:
			// Default fallback for any other setup commands
			reply = []byte("+OK\r\n")
		}

		_, err = conn.Write(reply)
		if err != nil {
			return
		}
	}
}
