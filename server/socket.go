package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"time"
)

// The official client uses the socket for keep-alive and notifications. This
// server has no remote-control sessions; playback state is recorded over HTTP.
func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	decoded, e := base64.StdEncoding.DecodeString(key)
	if e != nil || len(decoded) != 16 || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.WriteHeader(400)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(501)
		return
	}
	conn, rw, e := hijacker.Hijack()
	if e != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if rw.Flush() != nil {
		return
	}
	writeFrame := func(op byte, payload []byte) error {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if e := rw.WriteByte(0x80 | op); e != nil {
			return e
		}
		if e := rw.WriteByte(byte(len(payload))); e != nil {
			return e
		}
		if _, e := rw.Write(payload); e != nil {
			return e
		}
		return rw.Flush()
	}
	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		a, e := rw.ReadByte()
		if e != nil {
			return
		}
		b, e := rw.ReadByte()
		if e != nil || a&0x70 != 0 || a&0x80 == 0 || b&0x80 == 0 {
			return
		}
		length := uint64(b & 127)
		if length == 126 {
			var size [2]byte
			if _, e = io.ReadFull(rw, size[:]); e != nil {
				return
			}
			length = uint64(binary.BigEndian.Uint16(size[:]))
		} else if length == 127 {
			return
		}
		if length > 65535 {
			return
		}
		var mask [4]byte
		if _, e = io.ReadFull(rw, mask[:]); e != nil {
			return
		}
		payload := make([]byte, int(length))
		if _, e = io.ReadFull(rw, payload); e != nil {
			return
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		switch a & 15 {
		case 8:
			writeFrame(8, nil)
			return
		case 9:
			if len(payload) > 125 {
				return
			}
			if writeFrame(10, payload) != nil {
				return
			}
		case 1:
			if strings.Contains(string(payload), "KeepAlive") {
				if writeFrame(1, []byte(`{"MessageType":"KeepAlive"}`)) != nil {
					return
				}
			}
		case 10:
		default:
			return
		}
	}
}
