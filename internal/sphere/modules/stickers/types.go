package stickers

import "src.solsynth.dev/sosys/lighthouse/internal/sphere/model"

// packResponse is a sticker pack plus its stickers. The shared wire type has no
// Stickers field, so the module adds it here to keep the C# JSON shape.
type packResponse struct {
	model.StickerPack
	Stickers []model.Sticker `json:"stickers"`
}

// stickerResponse is a sticker whose pack carries its sticker list, matching
// SnSticker.Pack serialization.
type stickerResponse struct {
	model.Sticker
	Pack *packResponse `json:"pack"`
}

// ownershipResponse is a sticker pack ownership with the embedded pack.
type ownershipResponse struct {
	model.StickerPackOwnership
	Pack *packResponse `json:"pack"`
}

type packRequest struct {
	IconID      *string `json:"icon_id"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Prefix      *string `json:"prefix"`
}

type stickerRequest struct {
	Slug    *string `json:"slug"`
	Name    *string `json:"name"`
	ImageID *string `json:"image_id"`
	Size    *int    `json:"size"`
	Mode    *int    `json:"mode"`
}

type batchLookupRequest struct {
	Placeholders []string `json:"placeholders"`
}

type batchRenderingSettingsRequest struct {
	StickerIDs []string `json:"sticker_ids"`
	Size       *int     `json:"size"`
	Mode       *int     `json:"mode"`
}

type reorderRequest struct {
	Items []model.StickerReorderItem `json:"items"`
}
