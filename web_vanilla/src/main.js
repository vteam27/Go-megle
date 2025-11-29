// Frontend module moved from inline index.html
const ws = new WebSocket("ws://" + location.host + "/ws");
const localVideo = document.getElementById("localVideo");
const remoteVideo = document.getElementById("remoteVideo");
const statusDiv = document.getElementById("status");
const nextBtn = document.getElementById("nextBtn");

let peerConnection;
let localStream;
let currentPartnerId;
let autoRequeue = true; // set to false if you don't want automatic re-matching

// Google's Public STUN servers
const rtcConfig = {
    iceServers: [{ urls: "stun:stun.l.google.com:19302" }]
};

// 1. Setup Media
async function startCamera() {
    try {
        localStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
        localVideo.srcObject = localStream;
    } catch (e) {
        alert("Camera access denied!");
    }
}
startCamera();

nextBtn.addEventListener('click', findMatch);

// 2. WebSocket Handling
ws.onopen = () => { statusDiv.innerText = "Server Connected. Click Start."; };

ws.onmessage = async (msg) => {
    const message = JSON.parse(msg.data);
    const event = message.event;
    const data = message.data;

    if (event === "waiting") {
        statusDiv.innerText = "Waiting for a partner...";
    }
    else if (event === "match_found") {
        handleMatchFound(data);
    }
    else if (event === "signal") {
        handleSignal(data);
    }
    else if (event === "partner_left") {
        // The partner explicitly left (pressed Next or disconnected)
        console.log('Partner left:', data);
        cleanupPeer('Partner left');
        // Optionally auto re-join the queue after a short delay
        if (autoRequeue) {
            setTimeout(() => {
                findMatch();
            }, 1000);
        }
    }
};

// 3. Match Logic
function findMatch() {
    // If we're currently connected to a partner, notify them first so
    // their client can cleanup (prevents a frozen remote video on their side).
    if (currentPartnerId) {
        try {
            ws.send(JSON.stringify({ event: "leave", data: { target_id: currentPartnerId } }));
        } catch (e) {
            console.warn('Failed to notify partner about leaving', e);
        }
    }

    // Then cleanup our local peer connection and UI, and join the queue
    if (peerConnection) peerConnection.close();
    cleanupPeer('Joining Queue...');
    statusDiv.innerText = "Joining Queue...";
    ws.send(JSON.stringify({ event: "find_match", data: {} }));
}

async function handleMatchFound(data) {
    statusDiv.innerText = "Match Found! Connecting...";
    currentPartnerId = data.partner_id;
    createPeerConnection();

    // If I am the initiator, I create the Offer
    if (data.initiator) {
        const offer = await peerConnection.createOffer();
        await peerConnection.setLocalDescription(offer);
        sendSignal("offer", offer);
    }
}

// 4. WebRTC Logic
function createPeerConnection() {
    peerConnection = new RTCPeerConnection(rtcConfig);

    // Add local stream
    localStream.getTracks().forEach(track => peerConnection.addTrack(track, localStream));

    // Handle Remote Stream
    peerConnection.ontrack = (event) => {
        remoteVideo.srcObject = event.streams[0];
    };

    // Handle ICE Candidates
    peerConnection.onicecandidate = (event) => {
        if (event.candidate) {
            sendSignal("candidate", event.candidate);
        }
    };

    // Detect connection state changes; cleanup when disconnected
    peerConnection.onconnectionstatechange = () => {
        const s = peerConnection.connectionState;
        if (s === 'disconnected' || s === 'failed' || s === 'closed') {
            cleanupPeer('Connection closed');
        }
    };
}

async function handleSignal(data) {
    // If we receive an offer while we don't yet have a peerConnection, create one.
    if (data.type === "offer") {
        if (!peerConnection) createPeerConnection();
        await peerConnection.setRemoteDescription(new RTCSessionDescription(data.payload));
        const answer = await peerConnection.createAnswer();
        await peerConnection.setLocalDescription(answer);
        sendSignal("answer", answer);
        return;
    }

    if (!peerConnection) {
        console.warn('Received signal but no peerConnection exists:', data.type);
        return;
    }

    if (data.type === "answer") {
        await peerConnection.setRemoteDescription(new RTCSessionDescription(data.payload));
    } else if (data.type === "candidate") {
        await peerConnection.addIceCandidate(new RTCIceCandidate(data.payload));
    }
}

// Cleanup helper for peer connection and UI
function cleanupPeer(statusText) {
    try {
        if (peerConnection) {
            try { peerConnection.close(); } catch (e) { /* ignore */ }
            peerConnection = null;
        }
    } finally {
        remoteVideo.srcObject = null;
        currentPartnerId = null;
        if (statusText) statusDiv.innerText = statusText;
    }
}

function sendSignal(type, payload) {
    ws.send(JSON.stringify({
        event: "signal",
        data: {
            target_id: currentPartnerId,
            type: type,
            payload: payload
        }
    }));
}

export {};
