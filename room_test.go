package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newTestConn dials a real WebSocket connection against a throwaway httptest
// server so Room's closeAllClients (which calls conn.Close()) has something
// genuinely closable to operate on, without needing the full protocol.
func newTestConn(t *testing.T) *websocket.Conn {
	t.Helper()

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test conn: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return conn
}

// waitFor polls cond until it's true or the timeout elapses, failing the
// test otherwise. Used instead of a single fixed sleep so tests aren't
// flaky under load while still bounding how long a failure takes to surface.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %v", timeout)
	}
}

func TestRoomIdleTimeoutRemovesUnjoinedRoom(t *testing.T) {
	rm := NewRoomManager(30 * time.Millisecond)

	if _, err := rm.CreateRoom("r1", 5*time.Minute); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	waitFor(t, 500*time.Millisecond, func() bool {
		_, ok := rm.GetRoom("r1")
		return !ok
	})
}

func TestRoomAddClientCancelsIdleTimer(t *testing.T) {
	rm := NewRoomManager(30 * time.Millisecond)

	room, err := rm.CreateRoom("r1", 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	client := NewClient(newTestConn(t))
	room.AddClient(client)

	// Sleep well past the idle period: the room must still be registered
	// because a client joined and the idle timer was cancelled.
	time.Sleep(100 * time.Millisecond)

	if _, ok := rm.GetRoom("r1"); !ok {
		t.Fatal("room was removed despite an active client")
	}

	room.RemoveClient(client)
}

func TestRoomTransferCompleteTearsDownAndRemovesRoom(t *testing.T) {
	rm := NewRoomManager(30 * time.Second)

	room, err := rm.CreateRoom("r1", 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	client := NewClient(newTestConn(t))
	room.AddClient(client)

	room.SignalTransferComplete()

	waitFor(t, 500*time.Millisecond, func() bool {
		_, ok := rm.GetRoom("r1")
		return !ok
	})

	// closeAllClients should have closed the connection; writing to it now
	// must fail.
	if err := client.conn.WriteMessage(websocket.TextMessage, []byte("x")); err == nil {
		t.Error("expected write to closed connection to fail")
	}
}

func TestRoomTransferTimeoutTearsDownRoom(t *testing.T) {
	rm := NewRoomManager(30 * time.Second)

	room, err := rm.CreateRoom("r1", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	client := NewClient(newTestConn(t))
	room.AddClient(client)

	waitFor(t, 500*time.Millisecond, func() bool {
		_, ok := rm.GetRoom("r1")
		return !ok
	})
}

func TestSignalTransferCompleteIsIdempotent(t *testing.T) {
	rm := NewRoomManager(30 * time.Second)
	room, err := rm.CreateRoom("r1", 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	// Must not block or panic even when called multiple times back to back.
	room.SignalTransferComplete()
	room.SignalTransferComplete()
	room.SignalTransferComplete()

	waitFor(t, 500*time.Millisecond, func() bool {
		_, ok := rm.GetRoom("r1")
		return !ok
	})
}

func TestRoomManagerRejectsDuplicateRoomID(t *testing.T) {
	rm := NewRoomManager(30 * time.Second)

	if _, err := rm.CreateRoom("dup", 5*time.Minute); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if _, err := rm.CreateRoom("dup", 5*time.Minute); err == nil {
		t.Error("expected error creating a room with a duplicate ID")
	}
}
