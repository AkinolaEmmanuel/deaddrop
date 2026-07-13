package main

import (
	"errors"
	"sync"
	"time"
)

type RoomManager struct {
	mu         sync.Mutex
	rooms      map[string]*Room
	idlePeriod time.Duration
}

func NewRoomManager(idlePeriod time.Duration) *RoomManager {
	return &RoomManager{
		rooms:      make(map[string]*Room),
		idlePeriod: idlePeriod,
	}
}

func (rm *RoomManager) CreateRoom(id string, timeout time.Duration) (*Room, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if _, exists := rm.rooms[id]; exists {
		return nil, errors.New("room already exists")
	}

	// Create cleanup callback that removes room from manager
	cleanup := func() {
		rm.DeleteRoom(id)
	}

	// destroy and cleanup both remove the room from the manager's map; destroy
	// fires if no peer ever joins (idle timeout), cleanup fires once a transfer
	// completes or the post-join transfer timeout elapses.
	newRoom := NewRoom(id, rm.idlePeriod, cleanup, cleanup, timeout)

	rm.rooms[id] = newRoom

	return newRoom, nil
}

func (rm *RoomManager) GetRoom(id string) (*Room, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	room, exists := rm.rooms[id]
	return room, exists
}

func (rm *RoomManager) DeleteRoom(id string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	delete(rm.rooms, id)
}

func (rm *RoomManager) ListRooms() []*Room {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	list := make([]*Room, 0, len(rm.rooms))
	for _, room := range rm.rooms {
		list = append(list, room)
	}
	return list
}
