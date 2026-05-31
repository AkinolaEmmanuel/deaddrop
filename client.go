package main

import (
	"github.com/gorilla/websocket"
)

type Client struct {
	conn *websocket.Conn
	send chan []byte
}

func NewClient(conn *websocket.Conn) *Client {
	return &Client{
		conn: conn,
		send: make(chan []byte, 256),
	}
}

const (
	// maxMessageBytes caps a single WebSocket frame. SYNC frames carry an
	// entire encrypted file (base64-encoded inside JSON), so this must be
	// large enough for the files being transferred.
	maxMessageBytes = 32 << 20 // 32 MiB
)
