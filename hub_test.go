package main

import (
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestCreateAndRemoveMatch(t *testing.T) {
	// Initialize a test Redis client
	// Use localhost:6379 or skip Redis operations if unavailable
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisClient = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// Create a fresh hub
	h := &Hub{
		Clients:    make(map[string]*Client),
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
		Broadcast:  make(chan []byte),
		Matches:    make(map[string][2]string),
	}

	// Add two fake clients
	a := &Client{ID: "a", Send: make(chan []byte, 1)}
	b := &Client{ID: "b", Send: make(chan []byte, 1)}
	h.Clients[a.ID] = a
	h.Clients[b.ID] = b

	matchID := h.createMatch(a.ID, b.ID)

	if matchID == "" {
		t.Fatalf("expected non-empty matchID")
	}

	if a.CurrentMatch != matchID || b.CurrentMatch != matchID {
		t.Fatalf("expected both clients to have CurrentMatch set")
	}

	if len(h.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(h.Matches))
	}

	// Now remove
	h.removeMatch(matchID)

	if a.CurrentMatch != "" || b.CurrentMatch != "" {
		t.Fatalf("expected CurrentMatch to be cleared after remove")
	}

	if len(h.Matches) != 0 {
		t.Fatalf("expected 0 matches after remove, got %d", len(h.Matches))
	}
}
