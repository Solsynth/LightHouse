package model

// StickerPack is a sticker pack.
type StickerPack struct {
	ID          string     `json:"id"`
	Icon        any        `json:"icon"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Prefix      string     `json:"prefix"`
	Popularity  int        `json:"popularity"`
	PublisherID string     `json:"publisher_id"`
	Publisher   *Publisher `json:"publisher"`
	CreatedAt   *Time      `json:"created_at"`
	UpdatedAt   *Time      `json:"updated_at"`
	DeletedAt   *Time      `json:"deleted_at"`
}

// Sticker is one sticker in a pack.
type Sticker struct {
	ID                 string       `json:"id"`
	Slug               string       `json:"slug"`
	Name               string       `json:"name"`
	Image              any          `json:"image"`
	Size               int          `json:"size"`
	Mode               int          `json:"mode"`
	Order              int          `json:"order"`
	PackID             string       `json:"pack_id"`
	Pack               *StickerPack `json:"pack"`
	ResourceIdentifier string       `json:"resource_identifier"`
	CreatedAt          *Time        `json:"created_at"`
	UpdatedAt          *Time        `json:"updated_at"`
	DeletedAt          *Time        `json:"deleted_at"`
}

// StickerPackOwnership is one account's owned pack.
type StickerPackOwnership struct {
	ID        string       `json:"id"`
	PackID    string       `json:"pack_id"`
	AccountID string       `json:"account_id"`
	Order     int          `json:"order"`
	Pack      *StickerPack `json:"pack"`
	CreatedAt *Time        `json:"created_at"`
	UpdatedAt *Time        `json:"updated_at"`
	DeletedAt *Time        `json:"deleted_at"`
}

// StickerBatchLookupItem is one batch lookup result.
type StickerBatchLookupItem struct {
	Placeholder string   `json:"placeholder"`
	Sticker     *Sticker `json:"sticker"`
}

// StickerReorderItem is one entry of a reorder request.
type StickerReorderItem struct {
	ID    string `json:"id"`
	Order int    `json:"order"`
}
