package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"

	"deaddrop/gateway"
)

// ─── Client A: encrypt source file and push SYNC to the peer ────────────────

// SendSync reads the source file, encrypts it with AES-256-GCM, wraps it in a
// SYNC envelope, and enqueues it on the outbound send channel. The WritePump
// drains the channel onto the wire.
func (c *Client) SendSync(key []byte, sourcePath, roomID string) error {
	blob, err := gateway.EncryptFile(sourcePath, key)
	if err != nil {
		return fmt.Errorf("sendSync: encrypt %q: %w", sourcePath, err)
	}

	payload, err := json.Marshal(blob.ToCryptoPayload())
	if err != nil {
		return fmt.Errorf("sendSync: marshal payload: %w", err)
	}

	raw, err := json.Marshal(gateway.Message{
		Action:  gateway.ActionSync,
		RoomID:  roomID,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("sendSync: marshal envelope: %w", err)
	}

	select {
	case c.send <- raw:
		return nil
	default:
		return fmt.Errorf("sendSync: send channel full, could not enqueue SYNC")
	}
}

// runSender drives Client A's outbound flow: it sends the file once on connect,
// then watches it for local edits and re-sends a fresh SYNC on every settled
// change until stop is closed (connection torn down / transfer complete).
func (c *Client) runSender(key []byte, sourcePath, roomID string, stop <-chan struct{}) {
	if err := c.SendSync(key, sourcePath, roomID); err != nil {
		log.Printf("runSender: initial sync: %v", err)
		return
	}
	log.Printf("runSender: sent %q to room %q", sourcePath, roomID)

	changes := make(chan string, 1)
	watcher, err := NewFileWatcher(sourcePath, changes, stop)
	if err != nil {
		log.Printf("runSender: watcher init: %v", err)
		return
	}
	go watcher.Run()

	for {
		select {
		case <-stop:
			return
		case <-changes:
			if err := c.SendSync(key, sourcePath, roomID); err != nil {
				log.Printf("runSender: resync: %v", err)
				continue
			}
			log.Printf("runSender: re-sent %q after local edit", sourcePath)
		}
	}
}

// ─── Client B: send COMPLETE after successful decrypt+write ─────────────────

// sendComplete marshals a COMPLETE envelope and pushes it into the client's
// outbound send channel. The WritePump drains the channel onto the wire.
//
// Called immediately after NetworkWriteFile succeeds — the file is on disk,
// verified by GCM, and Client B's job is done.
func (c *Client) sendComplete(roomID string) error {
	msg := gateway.Message{
		Action: gateway.ActionComplete,
		RoomID: roomID,
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("sendComplete: marshal failed: %w", err)
	}

	select {
	case c.send <- raw:
		return nil
	default:
		return fmt.Errorf("sendComplete: send channel full, could not enqueue COMPLETE")
	}
}

// ─── Client B: full receive-decrypt-complete flow ───────────────────────────

// HandleSyncReceive is called from ReadPump when Client B receives a SYNC
// frame. It decrypts the payload, writes it atomically to disk, then fires
// the COMPLETE signal back up the pipe.
//
// key        — 32-byte AES key derived from the shared passphrase + roomID.
// destPath   — filesystem path where the decrypted file will be written.
// roomID     — passed straight into the COMPLETE envelope.
func (c *Client) HandleSyncReceive(
	msg *gateway.Message,
	key []byte,
	destPath string,
	roomID string,
) {
	cp, err := msg.DecodedPayload()
	if err != nil {
		log.Printf("handleSyncReceive: decode payload: %v", err)
		c.closeWithError("malformed payload")
		return
	}

	// Decrypt — GCM Open authenticates the ciphertext. Any key or data
	// mismatch returns ErrAuthFailed without touching destPath.
	plaintext, err := gateway.DecryptPayload(cp, key)
	if err != nil {
		log.Printf("handleSyncReceive: decryption failed: %v", err)
		c.closeWithError("decryption failed")
		return
	}

	// Write atomically through the network-write lock so the local
	// file watcher ignores this change.
	if err := NetworkWriteFile(destPath, plaintext, 0644); err != nil {
		log.Printf("handleSyncReceive: write to disk: %v", err)
		c.closeWithError("write failed")
		return
	}

	log.Printf("handleSyncReceive: file written to %q — sending COMPLETE", destPath)

	if err := c.sendComplete(roomID); err != nil {
		log.Printf("handleSyncReceive: %v", err)
	}
}

// closeWithError sends a WebSocket close frame with a plain-text reason
// and lets the WritePump drain before the connection dies.
func (c *Client) closeWithError(reason string) {
	msg := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, reason)
	c.conn.WriteMessage(websocket.CloseMessage, msg)
}

// ─── Server: catch COMPLETE and tear down the room ──────────────────────────

// handleComplete is called from ReadPump when any client sends ActionComplete.
//
// Flow:
//  1. Look up the room.
//  2. Signal transfer done — backgroundCleanup unblocks and calls
//     closeAllClients, which closes every connection in the room.
//  3. The manager's cleanup callback removes the room from the global map.
//     After this returns the room is unreachable and will be GC'd.
func (c *Client) handleComplete(rm *RoomManager, msg *gateway.Message) {
	if msg.RoomID == "" {
		log.Printf("handleComplete: missing roomId from client %p", c)
		return
	}

	room, ok := rm.GetRoom(msg.RoomID)
	if !ok {
		log.Printf("handleComplete: unknown room %q", msg.RoomID)
		return
	}

	log.Printf("handleComplete: room %q — transfer complete, tearing down", msg.RoomID)

	// Signal triggers backgroundCleanup's select case:
	//   case <-r.transferDone: → closeAllClients() → cleanup()
	room.SignalTransferComplete()
}

// ─── WritePump ───────────────────────────────────────────────────────────────

// WritePump drains c.send onto the WebSocket and handles the server-initiated
// close sequence.
//
// When backgroundCleanup calls closeAllClients → client.conn.Close(), the
// next WriteMessage call here returns an error and the loop exits, taking
// the goroutine with it cleanly.
func (c *Client) WritePump() {
	// Keepalive ping so intermediate proxies don't drop idle connections.
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				// Hub closed the channel — send a clean close frame.
				c.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("writePump: write error for client %p: %v", c, err)
				return
			}

		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("writePump: ping failed for client %p: %v", c, err)
				return
			}
		}
	}
}

// ─── ReadPump (complete, with COMPLETE branch wired in) ──────────────────────

// ReadPump is the definitive version of the inbound frame loop, now handling
// all three protocol actions: JOIN, SYNC, and COMPLETE.
func (c *Client) ReadPump(rm *RoomManager, key []byte, destPath, roomID string) {
	defer func() {
		c.conn.Close()
		log.Printf("readPump: client %p disconnected", c)
	}()

	c.conn.SetReadLimit(maxMessageBytes)

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
			) {
				log.Printf("readPump: unexpected close for client %p: %v", c, err)
			}
			return
		}

		var msg gateway.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("readPump: malformed envelope from client %p: %v", c, err)
			continue
		}

		switch msg.Action {
		case gateway.ActionJoin:
			// Handled at connection time (room lookup/creation).
			// Nothing to do here mid-session.

		case gateway.ActionSync:
			// Only the receiver (Client B) acts on SYNC: decrypt and write.
			// A receiver is identified by holding a decryption key. The sender
			// (Client A) never expects inbound SYNC frames, so it ignores them.
			if key != nil {
				c.HandleSyncReceive(&msg, key, destPath, roomID)
			} else {
				log.Printf("readPump: ignoring SYNC on sender connection (client %p)", c)
			}

		case gateway.ActionComplete:
			c.handleComplete(rm, &msg)

		default:
			log.Printf("readPump: unhandled action %q from client %p", msg.Action, c)
		}
	}
}

// ─── HTTP upgrade handler (entry point) ──────────────────────────────────────

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// Restrict to same origin in production.
		// For a local CLI tool, allow all — both peers are on trusted machines.
		return true
	},
}

// SenderConfig carries everything the server (Client A) needs to service a
// joining peer: the room manager and the specific room, plus the key and
// source file used to produce SYNC frames.
type SenderConfig struct {
	RM         *RoomManager
	Room       *Room
	Key        []byte
	SourcePath string
	RoomID     string
}

// ServeWS upgrades an HTTP connection to WebSocket, validates the JOIN frame,
// registers the peer with its room, starts the sender loop (encrypt + SYNC +
// watch for edits), and runs the read loop to catch COMPLETE.
func ServeWS(cfg *SenderConfig, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("serveWS: upgrade failed: %v", err)
		return
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	_, raw, err := conn.ReadMessage()
	if err != nil {
		log.Printf("serveWS: failed to read JOIN frame: %v", err)
		conn.Close()
		return
	}

	conn.SetReadDeadline(time.Time{})

	var msg gateway.Message
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Action != gateway.ActionJoin || msg.RoomID == "" {
		log.Printf("serveWS: invalid JOIN message: %v", msg.Action)
		conn.Close()
		return
	}

	if msg.RoomID != cfg.RoomID {
		log.Printf("serveWS: JOIN for unknown room %q (expected %q)", msg.RoomID, cfg.RoomID)
		conn.Close()
		return
	}

	client := NewClient(conn)
	cfg.Room.AddClient(client)
	log.Printf("serveWS: peer joined room %q", cfg.RoomID)

	// stop tears down the sender loop (and its file watcher) once the read
	// loop returns — i.e. when the peer disconnects or the room is torn down.
	stop := make(chan struct{})

	go client.WritePump()
	go client.runSender(cfg.Key, cfg.SourcePath, cfg.RoomID, stop)

	// Sender connection holds no decryption key (key == nil) — it only listens
	// for the peer's COMPLETE frame.
	client.ReadPump(cfg.RM, nil, "", cfg.RoomID)

	close(stop)
	cfg.Room.RemoveClient(client)
}

// ─── config ──────────────────────────────────────────────────────────────────

// transferTimeout bounds how long a room stays alive waiting for a transfer to
// complete before it is torn down.
const transferTimeout = 5 * time.Minute

// serverAddr is the listen address for Client A. Override with DEADDROP_ADDR
// (e.g. ":9000" or "0.0.0.0:8080").
func serverAddr() string {
	if a := os.Getenv("DEADDROP_ADDR"); a != "" {
		return a
	}
	return ":8080"
}

// dialURL is the WebSocket URL Client B dials. Override with DEADDROP_URL
// (e.g. "ws://192.168.1.10:8080/ws") to reach a peer on another machine.
func dialURL() string {
	if u := os.Getenv("DEADDROP_URL"); u != "" {
		return u
	}
	return "ws://localhost:8080/ws"
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	// ── Prompt ──────────────────────────────────────────────────────────────
	input, err := RunPrompt()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// ── Key derivation ──────────────────────────────────────────────────────
	key, err := gateway.DeriveKey(input.Passphrase, input.RoomID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "key derivation failed: %v\n", err)
		os.Exit(1)
	}

	rm := NewRoomManager(30 * time.Second)

	switch input.Action {
	case PromptCreate:
		// Client A: create the room, start the HTTP server, wait for Client B.
		room, err := rm.CreateRoom(input.RoomID, transferTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create room: %v\n", err)
			os.Exit(1)
		}

		cfg := &SenderConfig{
			RM:         rm,
			Room:       room,
			Key:        key,
			SourcePath: input.FilePath,
			RoomID:     input.RoomID,
		}

		addr := serverAddr()
		fmt.Printf("\n  Room %q open on %s. Waiting for peer...\n", input.RoomID, addr)

		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			ServeWS(cfg, w, r)
		})

		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("server error: %v", err)
		}

	case PromptJoin:
		// Client B: dial the server, receive SYNC, decrypt, send COMPLETE.
		url := dialURL()
		fmt.Printf("\n  Joining room %q at %s...\n", input.RoomID, url)

		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dial failed: %v\n", err)
			os.Exit(1)
		}

		client := NewClient(conn)

		// Send JOIN to register with the room.
		joinMsg, _ := json.Marshal(gateway.Message{
			Action: gateway.ActionJoin,
			RoomID: input.RoomID,
		})
		conn.WriteMessage(websocket.TextMessage, joinMsg)

		go client.WritePump()
		client.ReadPump(rm, key, "received_file", input.RoomID)
	}
}
