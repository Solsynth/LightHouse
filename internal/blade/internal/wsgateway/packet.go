package wsgateway

import (
	"encoding/json"

	gen "src.solsynth.dev/sosys/go/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	PacketTypePing  = "ping"
	PacketTypePong  = "pong"
	PacketTypeError = "error"
)

// Packet is the on-wire websocket packet structure (JSON over binary frame).
//
// AckID is stamped only on the JSON egress path for ack-tracked packet
// families (see ack.go). It never appears on the protobuf wire, so
// packetToProto/packetFromProto stay free of it.
type Packet struct {
	Type         string          `json:"type"`
	Data         json.RawMessage `json:"data,omitempty"`
	Endpoint     string          `json:"endpoint,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	AckID        string          `json:"_ack_id,omitempty"`
}

func packetFromProto(p *gen.DyWebSocketPacket) Packet {
	if p == nil {
		return Packet{}
	}

	pkt := Packet{
		Type: p.GetType(),
		Data: p.GetData(),
	}

	if p.GetErrorMessage() != nil {
		pkt.ErrorMessage = p.GetErrorMessage().GetValue()
	}

	return pkt
}

func packetToProto(p Packet) *gen.DyWebSocketPacket {
	pkt := &gen.DyWebSocketPacket{
		Type: p.Type,
		Data: p.Data,
	}
	if p.ErrorMessage != "" {
		pkt.ErrorMessage = wrapperspb.String(p.ErrorMessage)
	}
	return pkt
}
