// Package events ports Ring's event-driven integrations: the websocket_push
// publisher (RemoteWebSocketService) and the filesystem.file.updated.v1
// listener (FileMetadataReferenceListener).
package events

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/wrapperspb"

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/metoer/internal/grpcclient"
)

// PushSubject mirrors RemoteWebSocketService.PushSubject.
const PushSubject = "websocket_push"

// Solian is WebSocketNamespaces.Solian; Resolve mirrors
// WebSocketNamespaces.Resolve (empty/whitespace → Solian, else trimmed).
const Solian = "dev.solsynth.solian"

// ResolveNamespace mirrors WebSocketNamespaces.Resolve.
func ResolveNamespace(ns string) string {
	if strings.TrimSpace(ns) == "" {
		return Solian
	}
	return strings.TrimSpace(ns)
}

// wsPushPayload mirrors RemoteWebSocketService's WebSocketPushEvent.
// Packet is base64 (STJ encodes byte[]), carrying the UTF-8 protojson
// representation of DyWebSocketPacket.
type wsPushPayload struct {
	Namespace         string   `json:"namespace"`
	Target            string   `json:"target"`
	Ids               []string `json:"ids"`
	ExcludedDeviceIds []string `json:"excluded_device_ids"`
	Packet            []byte   `json:"packet"`
}

// WebSocketService mirrors RemoteWebSocketService: publishes
// websocket_push envelopes on core NATS and queries Blade for connected
// device ids.
type WebSocketService struct {
	Bus   *eb.Bus
	Blade *grpcclient.Clients
	Log   *slog.Logger
}

// CreatePacket mirrors RemoteWebSocketService.CreatePacket: a
// DyWebSocketPacket with the given type/data and (empty) error message.
func CreatePacket(eventType string, data []byte) *gen.DyWebSocketPacket {
	return &gen.DyWebSocketPacket{
		Type:         eventType,
		Data:         data,
		ErrorMessage: wrapperspb.String(""),
	}
}

// PushWebSocketPacket publishes an account-targeted websocket_push envelope
// (PushWebSocketPacket with excluded ids), mirroring PublishAccountPush.
func (s *WebSocketService) PushWebSocketPacket(ctx context.Context, accountID string, eventType string, data []byte, excludedDeviceIds []string, namespace string) error {
	return s.publishPush(ctx, wsPushPayload{
		Namespace:         ResolveNamespace(namespace),
		Target:            "account",
		Ids:               []string{accountID},
		ExcludedDeviceIds: distinctNonEmpty(excludedDeviceIds),
		Packet:            marshalPacket(CreatePacket(eventType, data)),
	})
}

func (s *WebSocketService) publishPush(ctx context.Context, payload wsPushPayload) error {
	if s == nil || s.Bus == nil || s.Bus.Conn == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.Bus.Conn.Publish(PushSubject, raw)
}

// marshalPacket returns the base64 of the protojson DyWebSocketPacket.
func marshalPacket(packet *gen.DyWebSocketPacket) []byte {
	data, err := protojson.Marshal(packet)
	if err != nil {
		return []byte{}
	}
	return []byte(base64.StdEncoding.EncodeToString(data))
}

func distinctNonEmpty(ids []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// GetConnectedWebsocketDeviceIds mirrors
// RemoteWebSocketService.GetConnectedWebsocketDeviceIds: asks Blade which of
// the given device ids are connected in the resolved namespace. Returns an
// error when the Blade client is unavailable (the caller degrades to an
// empty set).
func (s *WebSocketService) GetConnectedWebsocketDeviceIds(ctx context.Context, deviceIds []string, namespace string) ([]string, error) {
	if s == nil || s.Blade == nil || s.Blade.WebSocket() == nil {
		return nil, nil
	}
	ids := distinctNonEmpty(deviceIds)
	if len(ids) == 0 {
		return []string{}, nil
	}
	resp, err := s.Blade.WebSocket().GetConnectedWebsocketDeviceIds(ctx, &gen.DyGetConnectedWebsocketDeviceIdsRequest{
		DeviceIds: ids,
		Namespace: ResolveNamespace(namespace),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []string{}, nil
	}
	return resp.DeviceIds, nil
}
