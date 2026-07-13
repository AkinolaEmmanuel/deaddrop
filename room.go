package main

import (
	"sync"
	"time"
)

type Room struct {
	ID           string
	mu           sync.Mutex
	clients      map[*Client]bool
	idleTimer    *time.Timer
	idlePeriod   *time.Duration
	destroy      func()
	transferDone chan bool
	cleanup      func()
	timeout      time.Duration
}

func NewRoom(id string, idlePeriod time.Duration, destroy func(), cleanup func(), timeout time.Duration) *Room {
	r := &Room{
		ID:           id,
		clients:      make(map[*Client]bool),
		idlePeriod:   &idlePeriod,
		destroy:      destroy,
		cleanup:      cleanup,
		transferDone: make(chan bool, 1),
		timeout:      timeout,
	}

	r.idleTimer = time.AfterFunc(idlePeriod, r.selfDestruct)

	// Spin up background goroutine to handle transfer completion or timeout
	go r.backgroundCleanup()

	return r
}

func (r *Room) AddClient(c *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.clients[c] = true

	if r.idleTimer != nil {
		r.idleTimer.Stop()
		r.idleTimer = nil
	}
}

func (r *Room) RemoveClient(c *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.clients, c)

	if len(r.clients) == 0 {
		r.idleTimer = time.AfterFunc(*r.idlePeriod, r.selfDestruct)
	}
}

func (r *Room) selfDestruct() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.clients) == 0 && r.destroy != nil {
		r.destroy()

		// Wake backgroundCleanup now instead of leaving it parked until
		// transferTimeout — the room is already gone from the manager's map.
		select {
		case r.transferDone <- true:
		default:
		}
	}
}

func (r *Room) backgroundCleanup() {
	select {
	case <-r.transferDone:
		// Transfer completed successfully
		r.closeAllClients()
	case <-time.After(r.timeout):
		// Timeout reached
		r.closeAllClients()
	}

	// Cleanup callback to remove room from global map
	if r.cleanup != nil {
		r.cleanup()
	}
}

func (r *Room) closeAllClients() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for client := range r.clients {
		client.conn.Close()
		delete(r.clients, client)
	}
}

func (r *Room) SignalTransferComplete() {
	select {
	case r.transferDone <- true:
	default:
		// Already signaled or processed
	}
}
