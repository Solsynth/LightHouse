package autocomplete

import (
	"context"
	"encoding/json"

	gen "src.solsynth.dev/sosys/go/proto"
)

// grpcServer implements gen.DyAutocompletionServiceServer over the same
// suggestion engine the HTTP endpoint uses.
type grpcServer struct {
	gen.UnimplementedDyAutocompletionServiceServer
	svc *instance
}

// Autocomplete answers Messager's mention/sticker lookup. Suggestions are
// serialized into Data as the JSON wire shape of model.Autocompletion.
func (s *grpcServer) Autocomplete(ctx context.Context, request *gen.DyAutocompletionRequest) (*gen.DyAutocompletionResponse, error) {
	limit := int(request.GetLimit())
	if limit <= 0 {
		limit = DefaultLimit
	}

	results := s.svc.complete(ctx, request.GetContent(), limit)

	response := &gen.DyAutocompletionResponse{
		Results: make([]*gen.DyAutocompletionResult, 0, len(results)),
	}
	for _, result := range results {
		data, err := json.Marshal(result.Data)
		if err != nil {
			continue
		}
		response.Results = append(response.Results, &gen.DyAutocompletionResult{
			Type:    result.Type,
			Keyword: result.Keyword,
			Data:    string(data),
		})
	}
	return response, nil
}
