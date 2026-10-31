package rewind

import (
	"context"
	"encoding/json"
	"strings"

	"google.golang.org/grpc"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/grpcclient"
)

// Sources holds the optional downstream DyRewindService clients. Passport
// called the sphere and messager rewind services over its service-discovery
// HTTP handler; Stargate dials explicit targets from module settings.
type Sources struct {
	clients []rewindClient
	conns   []*grpc.ClientConn
}

type rewindClient struct {
	name   string
	client gen.DyRewindServiceClient
}

// NewSources dials every configured target. Empty targets are skipped, which
// makes those service keys absent from the stored document (documented gap).
func NewSources(settings Settings) (*Sources, error) {
	sources := &Sources{}
	for _, target := range []struct {
		name string
		addr string
	}{
		{name: "sphere", addr: settings.SphereGRPC},
		{name: "messager", addr: settings.MessagerGRPC},
	} {
		if target.addr == "" {
			continue
		}
		conn, err := grpcclient.Dial(target.addr)
		if err != nil {
			sources.Close()
			return nil, err
		}
		if conn == nil {
			continue
		}
		sources.conns = append(sources.conns, conn)
		sources.clients = append(sources.clients, rewindClient{
			name:   target.name,
			client: gen.NewDyRewindServiceClient(conn),
		})
	}
	return sources, nil
}

// Close releases the dialed connections.
func (s *Sources) Close() {
	if s == nil {
		return
	}
	for _, conn := range s.conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
	s.conns = nil
	s.clients = nil
}

// Events fetches one rewind event per configured service in parallel and
// decodes the JSON payload into the data map Passport stored. A failed RPC
// fails the whole point creation, mirroring the C# Task.WhenAll.
func (s *Sources) Events(ctx context.Context, accountID string, year int) (map[string]map[string]any, error) {
	if s == nil || len(s.clients) == 0 {
		return map[string]map[string]any{}, nil
	}
	result := make(map[string]map[string]any, len(s.clients))
	type outcome struct {
		name string
		data map[string]any
		err  error
	}
	results := make(chan outcome, len(s.clients))
	request := &gen.DyRequestRewindEvent{AccountId: accountID, Year: int32(year)}
	for _, source := range s.clients {
		go func(source rewindClient) {
			event, err := source.client.GetRewindEvent(ctx, request)
			if err != nil {
				results <- outcome{name: source.name, err: err}
				return
			}
			data, err := decodeEventData(event)
			results <- outcome{name: serviceID(event, source.name), data: data, err: err}
		}(source)
	}
	for range s.clients {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case got := <-results:
			if got.err != nil {
				return nil, got.err
			}
			result[got.name] = got.data
		}
	}
	return result, nil
}

// decodeEventData mirrors InfraObjectCoder.ConvertByteStringToObject: the
// protobuf `data` bytes are a UTF-8 JSON object.
func decodeEventData(event *gen.DyRewindEvent) (map[string]any, error) {
	if event == nil || len(event.GetData()) == 0 {
		return map[string]any{}, nil
	}
	data := map[string]any{}
	if err := json.Unmarshal(event.GetData(), &data); err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

func serviceID(event *gen.DyRewindEvent, fallback string) string {
	if event != nil {
		if id := strings.TrimSpace(event.GetServiceId()); id != "" {
			return id
		}
	}
	if fallback == "" {
		return "unknown"
	}
	return fallback
}
