package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

type serverStatus struct {
	Online  bool   `json:"online"`
	Players int    `json:"players"`
	Max     int    `json:"max"`
	Error   string `json:"error,omitempty"`
}

func writeVarInt(w *bytes.Buffer, v int) {
	u := uint32(v)
	for {
		if u&^0x7F == 0 {
			w.WriteByte(byte(u))
			return
		}
		w.WriteByte(byte(u&0x7F) | 0x80)
		u >>= 7
	}
}

func readVarInt(r io.Reader) (int, error) {
	var num int
	var shift uint
	for i := 0; i < 5; i++ {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		num |= int(b[0]&0x7F) << shift
		if b[0]&0x80 == 0 {
			return num, nil
		}
		shift += 7
	}
	return 0, fmt.Errorf("varint")
}

func pingServer(host string, port int) serverStatus {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 4*time.Second)
	if err != nil {
		return serverStatus{Error: "нет связи"}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	var payload bytes.Buffer
	writeVarInt(&payload, 767)
	hb := []byte(host)
	writeVarInt(&payload, len(hb))
	payload.Write(hb)
	_ = binary.Write(&payload, binary.BigEndian, uint16(port))
	writeVarInt(&payload, 1)
	if _, err := conn.Write(frame(0, payload.Bytes())); err != nil {
		return serverStatus{Error: "нет связи"}
	}
	if _, err := conn.Write(frame(0, nil)); err != nil {
		return serverStatus{Error: "нет связи"}
	}
	length, err := readVarInt(conn)
	if err != nil || length <= 0 || length > 1<<20 {
		return serverStatus{Error: "битый ответ"}
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return serverStatus{Error: "битый ответ"}
	}
	br := bytes.NewReader(buf)
	if _, err := readVarInt(br); err != nil {
		return serverStatus{Error: "битый ответ"}
	}
	n, err := readVarInt(br)
	if err != nil || n < 0 || n > br.Len() {
		return serverStatus{Error: "битый ответ"}
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(br, raw); err != nil {
		return serverStatus{Error: "битый ответ"}
	}
	var body struct {
		Players struct {
			Online int `json:"online"`
			Max    int `json:"max"`
		} `json:"players"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return serverStatus{Error: "битый ответ"}
	}
	return serverStatus{Online: true, Players: body.Players.Online, Max: body.Players.Max}
}

func frame(id int, payload []byte) []byte {
	var body bytes.Buffer
	writeVarInt(&body, id)
	body.Write(payload)
	var out bytes.Buffer
	writeVarInt(&out, body.Len())
	out.Write(body.Bytes())
	return out.Bytes()
}
