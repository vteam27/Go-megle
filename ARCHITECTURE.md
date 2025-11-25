# Architecture Overview

This document describes the high-level architecture and the core runtime flows for the Go-Megle demo.

## Components

- HTTP Server (Go) — serves the static `index.html` and upgrades `/ws` to WebSocket for signaling.
- Hub (in-memory) — maintains currently connected `Client` objects and their `Send` channels.
- Redis — used as a lightweight queue for matchmaking. A Lua script atomically pops or pushes IDs to the queue.
- Client (browser) — obtains media via `getUserMedia`, creates an RTCPeerConnection, and exchanges SDP/ICE through the WebSocket.

## Match flow

1. Client A sends `find_match` to server.
2. Server runs Lua match script; if no opponent, Client A is added to Redis queue.
3. When Client B joins and a match is found, server notifies both clients with `match_found` and `initiator` flags.
4. Initiator creates an offer and sends it via `signal` -> server forwards to target.
5. Other client answers; ICE candidates flow as `candidate` messages. PeerConnection completes.

## Disconnects and cleanup

- Clients can explicitly send `leave` to notify their partner. Server relays `partner_left` to the target so they can cleanup and optionally requeue.
- For production, server should track active matches in-memory or in Redis and ensure partner notification on abrupt disconnect (Unregister).

## Recommended production additions
- Persist active match metadata (matchID -> {a,b}) to Redis so any instance can reconcile state.
- Use Redis Pub/Sub or a messaging layer to relay signaling across multiple server instances.
- Add TURN server for RTC relay.
- Add TLS termination for secure contexts and wss.

