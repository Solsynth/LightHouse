package reader

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// grpcServer implements gen.DyWebReaderServiceServer for the fleet's internal
// callers.
type grpcServer struct {
	gen.UnimplementedDyWebReaderServiceServer
	svc *instance
}

func (s *grpcServer) ScrapeArticle(ctx context.Context, request *gen.DyScrapeArticleRequest) (*gen.DyScrapeArticleResponse, error) {
	rawURL := strings.TrimSpace(request.GetUrl())
	if rawURL == "" {
		return nil, status.Error(codes.InvalidArgument, "url is required")
	}

	article, err := s.svc.scrapeArticle(ctx, rawURL)
	if err != nil {
		return nil, mapGRPCError(err)
	}

	proto := &gen.DyScrapedArticle{LinkEmbed: linkEmbedToProto(article.LinkEmbed)}
	if article.Content != nil {
		proto.Content = article.Content
	}
	return &gen.DyScrapeArticleResponse{Article: proto}, nil
}

func (s *grpcServer) GetLinkPreview(ctx context.Context, request *gen.DyGetLinkPreviewRequest) (*gen.DyGetLinkPreviewResponse, error) {
	rawURL := strings.TrimSpace(request.GetUrl())
	if rawURL == "" {
		return nil, status.Error(codes.InvalidArgument, "url is required")
	}

	embed, err := s.svc.getLinkPreview(ctx, rawURL, request.GetBypassCache())
	if err != nil {
		return nil, mapGRPCError(err)
	}
	return &gen.DyGetLinkPreviewResponse{Preview: linkEmbedToProto(embed)}, nil
}

func (s *grpcServer) InvalidateLinkPreviewCache(ctx context.Context, request *gen.DyInvalidateLinkPreviewCacheRequest) (*gen.DyInvalidateLinkPreviewCacheResponse, error) {
	rawURL := strings.TrimSpace(request.GetUrl())
	if rawURL == "" {
		return nil, status.Error(codes.InvalidArgument, "url is required")
	}

	s.svc.invalidatePreview(ctx, rawURL)
	return &gen.DyInvalidateLinkPreviewCacheResponse{Success: true}, nil
}

// mapGRPCError turns a blocked or unreachable URL into InvalidArgument and
// everything else into Internal.
func mapGRPCError(err error) error {
	var readerErr *WebReaderError
	if errors.As(err, &readerErr) {
		return status.Error(codes.InvalidArgument, readerErr.Message)
	}
	return status.Error(codes.Internal, err.Error())
}

func linkEmbedToProto(embed model.LinkEmbed) *gen.DyLinkEmbed {
	return &gen.DyLinkEmbed{
		Url:         embed.URL,
		Title:       embed.Title,
		Description: embed.Description,
		ImageUrl:    embed.Image,
		FaviconUrl:  embed.Icon,
		SiteName:    embed.SiteName,
		ContentType: embed.Type,
	}
}
