package stickers

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

type stickerServer struct {
	gen.UnimplementedDyStickerServiceServer
	svc *service
}

func (s *stickerServer) GetStickerByIdentifier(ctx context.Context, request *gen.DyGetStickerRequest) (*gen.DySticker, error) {
	if strings.TrimSpace(request.GetIdentifier()) == "" {
		return nil, status.Error(codes.InvalidArgument, "identifier is required")
	}
	lookup, err := s.svc.lookup(ctx, request.GetIdentifier(), false)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if lookup == nil {
		return nil, status.Error(codes.NotFound, "sticker not found")
	}
	return stickerToProto(&lookup.Sticker, &lookup.Pack), nil
}

func stickerToProto(sticker *model.Sticker, pack *model.StickerPack) *gen.DySticker {
	proto := &gen.DySticker{
		Id:     sticker.ID,
		Slug:   sticker.Slug,
		Name:   sticker.Name,
		Size:   gen.DyStickerSize(sticker.Size + 1),
		Mode:   gen.DyStickerMode(sticker.Mode + 1),
		PackId: sticker.PackID,
		Order:  int32(sticker.Order),
	}
	if pack != nil && pack.Prefix != "" {
		proto.PackPrefix = new(pack.Prefix)
	}
	if sticker.CreatedAt != nil {
		proto.CreatedAt = timestamppb.New(sticker.CreatedAt.Time())
	}
	if sticker.UpdatedAt != nil {
		proto.UpdatedAt = timestamppb.New(sticker.UpdatedAt.Time())
	}
	if sticker.DeletedAt != nil {
		proto.DeletedAt = timestamppb.New(sticker.DeletedAt.Time())
	}

	file := imageRef(sticker.Image)
	if file == nil {
		return proto
	}
	proto.ImageId = file.Id
	if file.Name != "" {
		proto.ImageName = new(file.Name)
	}
	if file.MimeType != "" {
		proto.ImageMimeType = new(file.MimeType)
	}
	if file.Size > 0 {
		proto.ImageSize = new(file.Size)
	}
	if file.Url != "" {
		proto.ImageUrl = new(file.Url)
	}
	if file.Width != nil {
		proto.ImageWidth = new(*file.Width)
	}
	if file.Height != nil {
		proto.ImageHeight = new(*file.Height)
	}
	if file.Blurhash != "" {
		proto.ImageBlurhash = new(file.Blurhash)
	}
	return proto
}
