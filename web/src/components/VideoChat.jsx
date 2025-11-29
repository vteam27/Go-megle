import React, { useEffect, useRef, useState, useCallback } from 'react';

const rtcConfig = {
    iceServers: [{ urls: "stun:stun.l.google.com:19302" }]
};

export default function VideoChat() {
    const [status, setStatus] = useState("Connecting to server...");
    const [localStream, setLocalStream] = useState(null);
    const [isConnected, setIsConnected] = useState(false);
    
    const localVideoRef = useRef(null);
    const remoteVideoRef = useRef(null);
    const wsRef = useRef(null);
    const peerConnectionRef = useRef(null);
    const currentPartnerIdRef = useRef(null);

    // 1. Setup Media
    useEffect(() => {
        async function startCamera() {
            try {
                const stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
                setLocalStream(stream);
                if (localVideoRef.current) {
                    localVideoRef.current.srcObject = stream;
                }
            } catch (e) {
                alert("Camera access denied!");
                console.error(e);
            }
        }
        startCamera();
    }, []);

    // 2. WebSocket Handling
    useEffect(() => {
        const ws = new WebSocket("ws://" + window.location.host + "/ws");
        wsRef.current = ws;

        ws.onopen = () => {
            setStatus("Server Connected. Click Start.");
            setIsConnected(true);
        };

        ws.onmessage = async (msg) => {
            const message = JSON.parse(msg.data);
            const { event, data } = message;

            if (event === "waiting") {
                setStatus("Waiting for a partner...");
            } else if (event === "match_found") {
                handleMatchFound(data);
            } else if (event === "signal") {
                handleSignal(data);
            } else if (event === "partner_left") {
                console.log('Partner left:', data);
                cleanupPeer('Partner left');
                // Auto requeue after delay
                setTimeout(() => {
                    findMatch();
                }, 1000);
            }
        };

        return () => {
            ws.close();
        };
    }, [localStream]); // Re-run if localStream changes (though mostly stable)

    // 3. Match Logic
    const findMatch = useCallback(() => {
        if (currentPartnerIdRef.current) {
            try {
                wsRef.current.send(JSON.stringify({ 
                    event: "leave", 
                    data: { target_id: currentPartnerIdRef.current } 
                }));
            } catch (e) {
                console.warn('Failed to notify partner about leaving', e);
            }
        }

        if (peerConnectionRef.current) {
            peerConnectionRef.current.close();
        }
        cleanupPeer('Joining Queue...');
        setStatus("Joining Queue...");
        wsRef.current.send(JSON.stringify({ event: "find_match", data: {} }));
    }, []);

    const handleMatchFound = async (data) => {
        setStatus("Match Found! Connecting...");
        currentPartnerIdRef.current = data.partner_id;
        createPeerConnection();

        if (data.initiator) {
            const offer = await peerConnectionRef.current.createOffer();
            await peerConnectionRef.current.setLocalDescription(offer);
            sendSignal("offer", offer);
        }
    };

    // 4. WebRTC Logic
    const createPeerConnection = () => {
        const pc = new RTCPeerConnection(rtcConfig);
        peerConnectionRef.current = pc;

        if (localStream) {
            localStream.getTracks().forEach(track => pc.addTrack(track, localStream));
        }

        pc.ontrack = (event) => {
            if (remoteVideoRef.current) {
                remoteVideoRef.current.srcObject = event.streams[0];
            }
        };

        pc.onicecandidate = (event) => {
            if (event.candidate) {
                sendSignal("candidate", event.candidate);
            }
        };

        pc.onconnectionstatechange = () => {
            const s = pc.connectionState;
            if (s === 'disconnected' || s === 'failed' || s === 'closed') {
                cleanupPeer('Connection closed');
            }
        };
    };

    const handleSignal = async (data) => {
        if (data.type === "offer") {
            if (!peerConnectionRef.current) createPeerConnection();
            await peerConnectionRef.current.setRemoteDescription(new RTCSessionDescription(data.payload));
            const answer = await peerConnectionRef.current.createAnswer();
            await peerConnectionRef.current.setLocalDescription(answer);
            sendSignal("answer", answer);
            return;
        }

        if (!peerConnectionRef.current) {
            console.warn('Received signal but no peerConnection exists:', data.type);
            return;
        }

        if (data.type === "answer") {
            await peerConnectionRef.current.setRemoteDescription(new RTCSessionDescription(data.payload));
        } else if (data.type === "candidate") {
            await peerConnectionRef.current.addIceCandidate(new RTCIceCandidate(data.payload));
        }
    };

    const cleanupPeer = (statusText) => {
        if (peerConnectionRef.current) {
            try { peerConnectionRef.current.close(); } catch (e) { /* ignore */ }
            peerConnectionRef.current = null;
        }
        if (remoteVideoRef.current) {
            remoteVideoRef.current.srcObject = null;
        }
        currentPartnerIdRef.current = null;
        if (statusText) setStatus(statusText);
    };

    const sendSignal = (type, payload) => {
        wsRef.current.send(JSON.stringify({
            event: "signal",
            data: {
                target_id: currentPartnerIdRef.current,
                type: type,
                payload: payload
            }
        }));
    };

    return (
        <div>
            <div className="status">{status}</div>
            
            <div className="video-container">
                <video ref={localVideoRef} autoPlay playsInline muted />
                <video ref={remoteVideoRef} autoPlay playsInline />
            </div>

            <div className="controls">
                <button onClick={findMatch} disabled={!isConnected}>
                    Start / Next
                </button>
            </div>
        </div>
    );
}
