package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// --- Configuration ---
var redisClient *redis.Client
var ctx = context.Background()

// Simple Upgrader for WebSocket
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// --- Data Structures ---

// Client represents a connected user
type Client struct {
	ID     string
	Conn   *websocket.Conn
	Send   chan []byte
	Status string // "idle", "waiting", "matched"
	// CurrentMatch holds the match ID the client is currently in (if any)
	CurrentMatch string
}

// Message is the JSON payload for signaling
type Message struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"` // Generic payload
}

// MatchData is the payload for matchmaking
type MatchData struct {
	Tag string `json:"tag"`
}

// SignalData is the payload for WebRTC (SDP/ICE)
type SignalData struct {
	TargetID string          `json:"target_id"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}

// --- Global Hub (In-Memory Manager) ---
// In a real production system, this would be distributed via Redis Pub/Sub.
type Hub struct {
	Clients    map[string]*Client
	Register   chan *Client
	Unregister chan *Client
	Broadcast  chan []byte
	// Matches maps matchID -> pair of client IDs
	Matches map[string][2]string
	mu      sync.RWMutex
}

var hub = Hub{
	Clients:    make(map[string]*Client),
	Register:   make(chan *Client),
	Unregister: make(chan *Client),
	Broadcast:  make(chan []byte),
	Matches:    make(map[string][2]string),
}

// --- Redis Lua Script for Atomic Matching ---
// This script checks the queue. If someone is there, it matches and returns their ID.
// If empty, it adds the current user to the set.
var matchScript = redis.NewScript(`
	local queue_key = KEYS[1]
	local my_id = ARGV[1]
	
	-- 1. Try to pop a user from the set
	local opponent = redis.call("SPOP", queue_key)
	
	-- 2. If opponent found
	if opponent then
		if opponent == my_id then
			-- Edge case: Popped self (shouldn't happen often but possible in retries)
			redis.call("SADD", queue_key, my_id)
			return nil
		end
		return opponent
	end

	-- 3. If no opponent, add self to queue
	redis.call("SADD", queue_key, my_id)
	return nil
`)

func main() {
	// 1. Initialize Redis
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisClient = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// 2. Start Hub Routine
	go hub.run()

	// 3. Start Redis Pub/Sub listener for cross-instance signaling
	go startPubSubListener()

	// 4. Define Routes
	http.HandleFunc("/ws", serveWs)

	// Serve built frontend if present (web/dist). Otherwise fall back to the demo index.html
	if _, err := os.Stat("web/dist/index.html"); err == nil {
		// Serve the built SPA at root
		fs := http.FileServer(http.Dir("web/dist"))
		http.Handle("/", fs)
	} else {
		http.HandleFunc("/", serveHome)
		// Serve static web source under /web/ for convenience during development
		http.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.Dir("web"))))
	}

	log.Println("Server started on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

// --- Hub Logic ---
func (h *Hub) run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.Clients[client.ID] = client
			h.mu.Unlock()
			log.Printf("New Client: %s", client.ID)

		case client := <-h.Unregister:
			// On disconnect, remove from clients and if they were in a match,
			// notify their partner and cleanup the match.
			h.mu.Lock()
			if _, ok := h.Clients[client.ID]; ok {
				// If in a match, notify partner
				matchID := client.CurrentMatch

				// If no match in memory, check Redis (cross-instance support)
				if matchID == "" {
					if redisMatchID, ok := getClientMatchID(client.ID); ok {
						matchID = redisMatchID
					}
				}

				if matchID != "" {
					// Try to get match from memory first
					pair, ok := h.Matches[matchID]
					if !ok {
						// If not in memory, try Redis
						a, b, found := getMatchFromRedis(matchID)
						if found {
							pair = [2]string{a, b}
							ok = true
						}
					}

					if ok {
						var partnerID string
						if pair[0] == client.ID {
							partnerID = pair[1]
						} else {
							partnerID = pair[0]
						}

						// Try to notify local partner
						if partner, ok := h.Clients[partnerID]; ok {
							sendJSON(partner, "partner_left", map[string]string{"from": client.ID})
							partner.CurrentMatch = ""
						}

						// Remove match from both memory and Redis
						delete(h.Matches, matchID)
						deleteMatchFromRedis(matchID, pair[0], pair[1])
					}
				}

				delete(h.Clients, client.ID)
				close(client.Send)
				// Cleanup Redis queue and match references
				redisClient.SRem(ctx, "queue:general", client.ID)
				redisClient.Del(ctx, fmt.Sprintf("client:%s:match", client.ID))
			}
			h.mu.Unlock()
			log.Printf("Client Disconnected: %s", client.ID)
		}
	}
}

// createMatch links two client IDs into a match and updates their CurrentMatch.
// Also persists the match to Redis with a 10-minute TTL.
func (h *Hub) createMatch(aID, bID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	matchID := fmt.Sprintf("match_%d", time.Now().UnixNano())
	h.Matches[matchID] = [2]string{aID, bID}
	if a, ok := h.Clients[aID]; ok {
		a.CurrentMatch = matchID
		a.Status = "matched"
	}
	if b, ok := h.Clients[bID]; ok {
		b.CurrentMatch = matchID
		b.Status = "matched"
	}

	// Persist match to Redis for cross-instance coordination
	persistMatchToRedis(matchID, aID, bID)

	return matchID
}

// removeMatch removes a match by ID and clears clients' CurrentMatch fields.
// Also removes the match from Redis.
func (h *Hub) removeMatch(matchID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pair, ok := h.Matches[matchID]
	if !ok {
		return
	}
	for _, id := range pair {
		if c, ok := h.Clients[id]; ok {
			c.CurrentMatch = ""
			c.Status = "idle"
		}
	}
	delete(h.Matches, matchID)

	// Remove from Redis
	deleteMatchFromRedis(matchID, pair[0], pair[1])
}

// --- WebSocket Handler ---
func serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	// Create unique ID (simple time-based for demo)
	clientID := fmt.Sprintf("user_%d", time.Now().UnixNano())
	client := &Client{ID: clientID, Conn: conn, Send: make(chan []byte, 256), Status: "idle"}

	hub.Register <- client

	// Start goroutines for read/write
	go client.writePump()
	go client.readPump()
}

// --- Client Read Pump (The Logic Core) ---
func (c *Client) readPump() {
	defer func() {
		hub.Unregister <- c
		c.Conn.Close()
	}()

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			break
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Println("Invalid JSON:", err)
			continue
		}

		switch msg.Event {
		case "find_match":
			handleMatchRequest(c)
		case "signal":
			handleSignal(c, msg.Data)
		case "leave":
			handleLeave(c, msg.Data)
		}
	}
}

// Handle a leave/next event from a client and notify the target partner
func handleLeave(c *Client, data json.RawMessage) {
	var payload struct {
		TargetID string `json:"target_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}

	hub.mu.RLock()
	target, ok := hub.Clients[payload.TargetID]
	hub.mu.RUnlock()

	if ok {
		// Notify the target that their partner left
		sendJSON(target, "partner_left", map[string]string{"from": c.ID})
		// Cleanup match state if both were in the same match
		if c.CurrentMatch != "" {
			hub.removeMatch(c.CurrentMatch)
		}
	}
}

// --- Matchmaking Logic ---
func handleMatchRequest(c *Client) {
	c.Status = "waiting"
	queueKey := "queue:general" // Hardcoded for this demo

	// Run Lua Script
	cmd := matchScript.Run(ctx, redisClient, []string{queueKey}, c.ID)
	result, err := cmd.Result()

	if err != nil && err != redis.Nil {
		log.Println("Redis Error:", err)
		return
	}

	if result == nil {
		// No match found, we are now in the queue
		sendJSON(c, "waiting", map[string]string{"message": "Searching for partner..."})
	} else {
		// Match found!
		opponentID := result.(string)
		log.Printf("MATCH: %s <--> %s", c.ID, opponentID)

		hub.mu.RLock()
		opponent, ok := hub.Clients[opponentID]
		hub.mu.RUnlock()

		if ok {
			// Create server-side match tracking
			matchID := hub.createMatch(c.ID, opponentID)

			// Notify Me (Initiator)
			sendJSON(c, "match_found", map[string]interface{}{
				"partner_id": opponentID,
				"initiator":  true, // Tells frontend to create WebRTC Offer
				"match_id":   matchID,
			})

			// Notify Opponent
			sendJSON(opponent, "match_found", map[string]interface{}{
				"partner_id": c.ID,
				"initiator":  false,
				"match_id":   matchID,
			})
		} else {
			// Opponent disconnected while in queue? Try again.
			handleMatchRequest(c)
		}
	}
}

// --- Signaling Logic (Relay) ---
func handleSignal(c *Client, data json.RawMessage) {
	var signal SignalData
	json.Unmarshal(data, &signal)

	// Prepare signal message for Pub/Sub
	sigMsg := SignalMessage{
		TargetID: signal.TargetID,
		SenderID: c.ID,
		Type:     signal.Type,
		Payload:  signal.Payload,
	}

	// Publish to Redis Pub/Sub for cross-instance delivery
	msgBytes, _ := json.Marshal(sigMsg)
	if err := redisClient.Publish(ctx, "signals", msgBytes).Err(); err != nil {
		log.Printf("Failed to publish signal to Redis: %v", err)
	}

	// Also try local delivery (optimization for same-instance)
	hub.mu.RLock()
	target, ok := hub.Clients[signal.TargetID]
	hub.mu.RUnlock()

	if ok {
		// Forward the signal to the specific target locally
		outEvent := "signal"
		outPayload := map[string]interface{}{
			"sender_id": c.ID,
			"type":      signal.Type,
			"payload":   signal.Payload,
		}

		finalBytes, _ := json.Marshal(map[string]interface{}{
			"event": outEvent,
			"data":  outPayload,
		})

		target.Send <- finalBytes
	}
}

// --- Client Write Pump ---
func (c *Client) writePump() {
	for {
		select {
		case message, ok := <-c.Send:
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.Conn.WriteMessage(websocket.TextMessage, message)
		}
	}
}

// Helper
func sendJSON(c *Client, event string, data interface{}) {
	raw, _ := json.Marshal(data)
	msg := map[string]interface{}{
		"event": event,
		"data":  json.RawMessage(raw),
	}
	bytes, _ := json.Marshal(msg)
	c.Send <- bytes
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	// Serve the development frontend entry (Vite) when no built dist is present.
	http.ServeFile(w, r, "web/index.html")
}

// --- Redis Persistence Functions ---

// persistMatchToRedis stores match metadata in Redis with 10-minute TTL
func persistMatchToRedis(matchID, aID, bID string) {
	pipe := redisClient.Pipeline()

	// Store match metadata: match:<matchID> -> {a: aID, b: bID}
	pipe.HSet(ctx, fmt.Sprintf("match:%s", matchID), "a", aID, "b", bID)
	pipe.Expire(ctx, fmt.Sprintf("match:%s", matchID), 10*time.Minute)

	// Store reverse mapping: client:<clientID>:match -> matchID
	pipe.Set(ctx, fmt.Sprintf("client:%s:match", aID), matchID, 10*time.Minute)
	pipe.Set(ctx, fmt.Sprintf("client:%s:match", bID), matchID, 10*time.Minute)

	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("Failed to persist match to Redis: %v", err)
	}
}

// deleteMatchFromRedis removes match metadata from Redis
func deleteMatchFromRedis(matchID, aID, bID string) {
	pipe := redisClient.Pipeline()
	pipe.Del(ctx, fmt.Sprintf("match:%s", matchID))
	pipe.Del(ctx, fmt.Sprintf("client:%s:match", aID))
	pipe.Del(ctx, fmt.Sprintf("client:%s:match", bID))

	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("Failed to delete match from Redis: %v", err)
	}
}

// getMatchFromRedis retrieves match metadata from Redis
func getMatchFromRedis(matchID string) (string, string, bool) {
	result := redisClient.HGetAll(ctx, fmt.Sprintf("match:%s", matchID))
	data, err := result.Result()
	if err != nil || len(data) == 0 {
		return "", "", false
	}
	return data["a"], data["b"], true
}

// getClientMatchID retrieves the matchID for a client from Redis
func getClientMatchID(clientID string) (string, bool) {
	result := redisClient.Get(ctx, fmt.Sprintf("client:%s:match", clientID))
	matchID, err := result.Result()
	if err != nil {
		return "", false
	}
	return matchID, true
}

// --- Redis Pub/Sub for Cross-Instance Signaling ---

// SignalMessage represents a signaling message to be published/subscribed
type SignalMessage struct {
	TargetID string          `json:"target_id"`
	SenderID string          `json:"sender_id"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}

// startPubSubListener subscribes to Redis 'signals' channel and forwards messages to local clients
func startPubSubListener() {
	pubsub := redisClient.Subscribe(ctx, "signals")
	defer pubsub.Close()

	log.Println("Redis Pub/Sub listener started on channel 'signals'")

	for {
		msg, err := pubsub.ReceiveMessage(ctx)
		if err != nil {
			log.Printf("Pub/Sub error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		var sigMsg SignalMessage
		if err := json.Unmarshal([]byte(msg.Payload), &sigMsg); err != nil {
			log.Printf("Invalid signal message from Pub/Sub: %v", err)
			continue
		}

		// Check if target is connected to this instance
		hub.mu.RLock()
		target, ok := hub.Clients[sigMsg.TargetID]
		hub.mu.RUnlock()

		if ok {
			// Forward to local client
			outPayload := map[string]interface{}{
				"sender_id": sigMsg.SenderID,
				"type":      sigMsg.Type,
				"payload":   sigMsg.Payload,
			}

			finalBytes, _ := json.Marshal(map[string]interface{}{
				"event": "signal",
				"data":  outPayload,
			})

			target.Send <- finalBytes
		}
	}
}
