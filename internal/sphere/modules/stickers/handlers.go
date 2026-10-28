package stickers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func parseID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		c.Status(http.StatusNotFound)
		return uuid.Nil, false
	}
	return id, true
}

func (s *service) packResponse(entity *store.StickerPackEntity, stickers []store.StickerEntity, publisher *model.Publisher) packResponse {
	return packResponse{
		StickerPack: packWire(entity, stickers, publisher),
		Stickers:    stickerItems(entity, stickers),
	}
}

func stickerItems(_ *store.StickerPackEntity, stickers []store.StickerEntity) []model.Sticker {
	items := make([]model.Sticker, 0, len(stickers))
	for i := range stickers {
		// SnSticker.Pack is [JsonIgnore] on the C# side: the embedded pack is
		// not repeated inside a pack's sticker list.
		items = append(items, store.StickerToModel(&stickers[i]))
	}
	return items
}
func (s *service) lookupResponse(lookup *cachedLookup) stickerResponse {
	pack := &packResponse{StickerPack: lookup.Pack, Stickers: []model.Sticker{}}
	return stickerResponse{Sticker: lookup.Sticker, Pack: pack}
}

func (s *service) listPacks(c *gin.Context) {
	page, ok := httpx.PageQuery(c, maxTake(s.core))
	if !ok {
		return
	}

	var publisher *store.PublisherEntity
	if pubName, hasPub := c.GetQuery("pub"); hasPub {
		var entity store.PublisherEntity
		err := s.db.WithContext(c.Request.Context()).
			Where("LOWER(name) = ?", strings.ToLower(pubName)).
			First(&entity).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				httpx.Fail(c, errs.New("PUBLISHER_NOT_FOUND", "Publisher not found.", http.StatusNotFound))
				return
			}
			httpx.Internal(c)
			return
		}
		publisher = &entity
	}

	query := s.db.WithContext(c.Request.Context()).Model(&store.StickerPackEntity{})
	if publisher != nil {
		query = query.Where("publisher_id = ?", publisher.ID)
	}
	switch c.Query("order") {
	case "":
	case "usage":
		query = query.Order("popularity DESC")
	default:
		query = query.Order("created_at DESC")
	}
	if search := strings.TrimSpace(c.Query("query")); search != "" {
		pattern := "%" + search + "%"
		query = query.Where("(name ILIKE ? OR description ILIKE ?)", pattern, pattern)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var packs []store.StickerPackEntity
	if err := query.Offset(page.Offset).Limit(page.Take).Find(&packs).Error; err != nil {
		httpx.Internal(c)
		return
	}

	packIDs := make([]uuid.UUID, 0, len(packs))
	for _, pack := range packs {
		packIDs = append(packIDs, pack.ID)
	}
	stickersByPack, err := s.stickersForPacks(c.Request.Context(), packIDs)
	if err != nil {
		httpx.Internal(c)
		return
	}
	publishers := s.publishersFor(c, packs)

	items := make([]packResponse, 0, len(packs))
	for i := range packs {
		stickers := stickersByPack[packs[i].ID]
		if len(stickers) > 8 {
			stickers = stickers[:8]
		}
		items = append(items, s.packResponse(&packs[i], stickers, publishers[packs[i].PublisherID]))
	}
	httpx.List(c, items, total)
}

func (s *service) publishersFor(c *gin.Context, packs []store.StickerPackEntity) map[uuid.UUID]*model.Publisher {
	result := map[uuid.UUID]*model.Publisher{}
	for _, pack := range packs {
		if _, ok := result[pack.PublisherID]; ok {
			continue
		}
		result[pack.PublisherID] = s.publisherModel(c.Request.Context(), pack.PublisherID)
	}
	return result
}

func (s *service) listOwnedPacks(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	var ownerships []store.StickerPackOwnershipEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("account_id = ?", accountID).
		Order(`"order" ASC`).
		Find(&ownerships).Error; err != nil {
		httpx.Internal(c)
		return
	}

	packIDs := make([]uuid.UUID, 0, len(ownerships))
	for _, ownership := range ownerships {
		packIDs = append(packIDs, ownership.PackID)
	}
	packs, err := s.loadPacks(c, packIDs)
	if err != nil {
		httpx.Internal(c)
		return
	}
	stickersByPack, err := s.stickersForPacks(c.Request.Context(), packIDs)
	if err != nil {
		httpx.Internal(c)
		return
	}
	packList := make([]store.StickerPackEntity, 0, len(packs))
	for _, pack := range packs {
		packList = append(packList, *pack)
	}
	publishers := s.publishersFor(c, packList)

	items := make([]ownershipResponse, 0, len(ownerships))
	for i := range ownerships {
		pack := packs[ownerships[i].PackID]
		if pack == nil {
			items = append(items, ownershipResponse{StickerPackOwnership: ownershipWire(&ownerships[i], nil, nil, nil)})
			continue
		}
		response := ownershipResponse{
			StickerPackOwnership: ownershipWire(&ownerships[i], pack, stickersByPack[pack.ID], publishers[pack.PublisherID]),
		}
		response.Pack = ptrPack(s.packResponse(pack, stickersByPack[pack.ID], publishers[pack.PublisherID]))
		items = append(items, response)
	}
	httpx.OK(c, items)
}

func ptrPack(pack packResponse) *packResponse { return &pack }

func (s *service) loadPacks(c *gin.Context, ids []uuid.UUID) (map[uuid.UUID]*store.StickerPackEntity, error) {
	result := map[uuid.UUID]*store.StickerPackEntity{}
	if len(ids) == 0 {
		return result, nil
	}
	var packs []store.StickerPackEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id IN ?", ids).Find(&packs).Error; err != nil {
		return nil, err
	}
	for i := range packs {
		result[packs[i].ID] = &packs[i]
	}
	return result, nil
}

func (s *service) getPack(c *gin.Context) {
	id, ok := parseID(c, "packId")
	if !ok {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	publisher := s.publisherModel(c.Request.Context(), pack.PublisherID)
	httpx.OK(c, s.packResponse(pack, nil, publisher))
}

func (s *service) getPackByPrefix(c *gin.Context) {
	prefix := c.Param("prefix")
	var pack store.StickerPackEntity
	err := s.db.WithContext(c.Request.Context()).Where("prefix = ?", prefix).First(&pack).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	publisher := s.publisherModel(c.Request.Context(), pack.PublisherID)
	httpx.OK(c, s.packResponse(&pack, nil, publisher))
}

func (s *service) createPack(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	var request packRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Name == nil || *request.Name == "" {
		httpx.BadRequest(c, "STICKER_PACK_NAME_REQUIRED", "Name is required")
		return
	}
	if request.Prefix == nil || *request.Prefix == "" {
		httpx.BadRequest(c, "STICKER_PACK_PREFIX_REQUIRED", "Prefix is required")
		return
	}
	if !isAlphanumeric(*request.Prefix) {
		httpx.BadRequest(c, "STICKER_PACK_PREFIX_INVALID", "Prefix must only contain ASCII characters")
		return
	}
	pubName, _ := c.GetQuery("pub")
	if pubName == "" {
		httpx.BadRequest(c, "PUBLISHER_NOT_FOUND", "Publisher not found")
		return
	}
	var publisher store.PublisherEntity
	err = s.db.WithContext(c.Request.Context()).
		Where("LOWER(name) = ? AND account_id = ?", strings.ToLower(pubName), accountID).
		First(&publisher).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			httpx.BadRequest(c, "PUBLISHER_NOT_FOUND", "Publisher not found")
			return
		}
		httpx.Internal(c)
		return
	}

	pack := store.StickerPackEntity{
		ID:          uuid.New(),
		Name:        *request.Name,
		Description: stringValue(request.Description),
		Prefix:      *request.Prefix,
		PublisherID: publisher.ID,
	}
	if request.IconID != nil {
		file, err := s.resolveFile(c.Request.Context(), *request.IconID)
		if err != nil || file == nil {
			httpx.BadRequest(c, "STICKER_ICON_NOT_FOUND", "Icon not found.")
			return
		}
		pack.Icon = store.ToJSON(file)
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&pack).Error; err != nil {
		httpx.Internal(c)
		return
	}
	publisherModel := s.publisherModel(c.Request.Context(), pack.PublisherID)
	httpx.OK(c, s.packResponse(&pack, nil, publisherModel))
}

func (s *service) updatePack(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseID(c, "packId")
	if !ok {
		return
	}
	var request packRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), pack.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		if _, isMember := s.memberRole(c.Request.Context(), pack.PublisherID, accountID); !isMember {
			httpx.Fail(c, errs.New("FORBIDDEN", "You are not a member of this publisher", http.StatusForbidden))
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", "You need to be at least an editor to update sticker packs", http.StatusForbidden))
		return
	}

	if request.Name != nil {
		pack.Name = *request.Name
	}
	if request.Description != nil {
		pack.Description = *request.Description
	}
	if request.Prefix != nil {
		if !isAlphanumeric(*request.Prefix) {
			httpx.BadRequest(c, "STICKER_PACK_PREFIX_INVALID", "Prefix must only contain ASCII characters")
			return
		}
		pack.Prefix = *request.Prefix
	}
	if request.IconID != nil {
		file, err := s.resolveFile(c.Request.Context(), *request.IconID)
		if err != nil || file == nil {
			httpx.BadRequest(c, "STICKER_ICON_NOT_FOUND", "Icon not found.")
			return
		}
		pack.Icon = store.ToJSON(file)
	}
	if err := s.db.WithContext(c.Request.Context()).Save(pack).Error; err != nil {
		httpx.Internal(c)
		return
	}
	publisher := s.publisherModel(c.Request.Context(), pack.PublisherID)
	httpx.OK(c, s.packResponse(pack, nil, publisher))
}

func (s *service) deletePack(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	id, ok := parseID(c, "packId")
	if !ok {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), pack.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		if _, isMember := s.memberRole(c.Request.Context(), pack.PublisherID, accountID); !isMember {
			httpx.Fail(c, errs.New("FORBIDDEN", "You are not a member of this publisher", http.StatusForbidden))
			return
		}
		httpx.Fail(c, errs.New("FORBIDDEN", "You need to be an editor to delete sticker packs", http.StatusForbidden))
		return
	}

	var stickers []store.StickerEntity
	if err := s.db.WithContext(c.Request.Context()).Where("pack_id = ?", pack.ID).Find(&stickers).Error; err != nil {
		httpx.Internal(c)
		return
	}
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if len(stickers) > 0 {
			if err := tx.Where("pack_id = ?", pack.ID).Delete(&store.StickerEntity{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(pack).Error
	})
	if err != nil {
		httpx.Internal(c)
		return
	}
	s.purgeStickers(c.Request.Context(), stickers, map[uuid.UUID]store.StickerPackEntity{pack.ID: *pack})
	httpx.NoContent(c)
}

func (s *service) listStickers(c *gin.Context) {
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	var stickers []store.StickerEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("pack_id = ?", packID).
		Order(`"order" ASC, created_at ASC`).
		Find(&stickers).Error; err != nil {
		httpx.Internal(c)
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	httpx.OK(c, s.stickerList(stickers, pack))
}

func (s *service) stickerList(stickers []store.StickerEntity, pack *store.StickerPackEntity) []stickerResponse {
	items := make([]stickerResponse, 0, len(stickers))
	packModel := store.StickerPackToModel(pack)
	for i := range stickers {
		sticker := store.StickerToModel(&stickers[i])
		copied := packModel
		sticker.Pack = &copied
		items = append(items, stickerResponse{
			Sticker: sticker,
			Pack:    &packResponse{StickerPack: copied, Stickers: []model.Sticker{}},
		})
	}
	return items
}

func (s *service) getSticker(c *gin.Context) {
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var sticker store.StickerEntity
	err := s.db.WithContext(c.Request.Context()).
		Where("pack_id = ? AND id = ?", packID, id).
		First(&sticker).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	items := s.stickerList([]store.StickerEntity{sticker}, pack)
	httpx.OK(c, items[0])
}

func (s *service) lookupSticker(c *gin.Context) {
	lookup, err := s.lookup(c.Request.Context(), c.Param("identifier"), true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if lookup == nil {
		c.Status(http.StatusNotFound)
		return
	}
	httpx.OK(c, s.lookupResponse(lookup))
}

func (s *service) openSticker(c *gin.Context) {
	lookup, err := s.lookup(c.Request.Context(), c.Param("identifier"), true)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if lookup == nil {
		c.Status(http.StatusNotFound)
		return
	}
	file := imageRef(lookup.Sticker.Image)
	if file == nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Redirect(http.StatusFound, "/drive/files/"+file.Id+"?original=true")
}

func (s *service) lookupBatch(c *gin.Context) {
	var request batchLookupRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	placeholders := make([]string, 0, len(request.Placeholders))
	for _, placeholder := range request.Placeholders {
		if strings.TrimSpace(placeholder) == "" {
			continue
		}
		if len(placeholders) >= 100 {
			break
		}
		placeholders = append(placeholders, placeholder)
	}

	items := make([]model.StickerBatchLookupItem, 0, len(placeholders))
	for _, placeholder := range placeholders {
		lookup, err := s.lookup(c.Request.Context(), placeholder, true)
		if err != nil {
			httpx.Internal(c)
			return
		}
		item := model.StickerBatchLookupItem{Placeholder: placeholder}
		if lookup != nil {
			item.Sticker = &lookup.Sticker
		}
		items = append(items, item)
	}
	httpx.OK(c, items)
}

func (s *service) searchStickers(c *gin.Context) {
	search := c.Query("query")
	if search == "" {
		httpx.BadRequest(c, "STICKER_SEARCH_QUERY_REQUIRED", "query is required.")
		return
	}
	take := parseQueryInt(c, "take", 10)
	offset := parseQueryInt(c, "offset", 0)

	pattern := search + "%"
	query := s.db.WithContext(c.Request.Context()).
		Model(&store.StickerEntity{}).
		Joins("JOIN sticker_packs ON sticker_packs.id = stickers.pack_id AND sticker_packs.deleted_at IS NULL").
		Where(`(sticker_packs.prefix || '+' || stickers.slug) LIKE ?`, pattern)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var stickers []store.StickerEntity
	if err := query.Select("stickers.*").Order("stickers.created_at DESC").Offset(offset).Limit(take).Find(&stickers).Error; err != nil {
		httpx.Internal(c)
		return
	}

	packIDs := make([]uuid.UUID, 0, len(stickers))
	for _, sticker := range stickers {
		packIDs = append(packIDs, sticker.PackID)
	}
	packs, err := s.loadPacks(c, packIDs)
	if err != nil {
		httpx.Internal(c)
		return
	}
	items := make([]stickerResponse, 0, len(stickers))
	for i := range stickers {
		pack := packs[stickers[i].PackID]
		if pack == nil {
			continue
		}
		packModel := store.StickerPackToModel(pack)
		sticker := store.StickerToModel(&stickers[i])
		sticker.Pack = &packModel
		items = append(items, stickerResponse{
			Sticker: sticker,
			Pack:    &packResponse{StickerPack: packModel, Stickers: []model.Sticker{}},
		})
	}
	httpx.List(c, items, total)
}

func (s *service) createSticker(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	var request stickerRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Slug == nil || strings.TrimSpace(*request.Slug) == "" {
		httpx.BadRequest(c, "STICKER_SLUG_REQUIRED", "Slug is required.")
		return
	}
	if !isAlphanumeric(*request.Slug) {
		httpx.BadRequest(c, "STICKER_SLUG_INVALID", "Slug must only contain ASCII characters")
		return
	}
	if request.ImageID == nil {
		httpx.BadRequest(c, "STICKER_IMAGE_REQUIRED", "Image is required.")
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			httpx.Fail(c, errs.New("STICKER_PACK_NOT_FOUND", "Sticker pack not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), pack.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You are not a member of this publisher", http.StatusForbidden))
		return
	}
	var count int64
	if err := s.db.WithContext(c.Request.Context()).Model(&store.StickerEntity{}).
		Where("pack_id = ?", packID).Count(&count).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if count >= maxStickersPerPack {
		httpx.BadRequest(c, "STICKER_PACK_CAPACITY_REACHED", "Sticker pack has reached maximum capacity of 24 stickers.")
		return
	}
	file, err := s.resolveFile(c.Request.Context(), *request.ImageID)
	if err != nil || file == nil {
		httpx.BadRequest(c, "STICKER_IMAGE_NOT_FOUND", "Image not found.")
		return
	}

	name := *request.Slug
	if request.Name != nil {
		name = *request.Name
	}
	size := int(model.StickerSizeAuto)
	if request.Size != nil {
		size = *request.Size
	}
	mode := int(model.StickerModeSticker)
	if request.Mode != nil {
		mode = *request.Mode
	}
	sticker := store.StickerEntity{
		ID:     uuid.New(),
		Slug:   *request.Slug,
		Name:   name,
		Image:  store.ToJSON(file),
		Size:   size,
		Mode:   mode,
		PackID: packID,
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&sticker).Error; err != nil {
		httpx.Internal(c)
		return
	}
	items := s.stickerList([]store.StickerEntity{sticker}, pack)
	httpx.OK(c, items[0])
}

func (s *service) updateSticker(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var request stickerRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), pack.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You are not a member of this publisher", http.StatusForbidden))
		return
	}
	var sticker store.StickerEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id = ? AND pack_id = ?", id, packID).First(&sticker).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if request.Slug != nil {
		if !isAlphanumeric(*request.Slug) {
			httpx.BadRequest(c, "STICKER_SLUG_INVALID", "Slug must only contain ASCII characters")
			return
		}
		sticker.Slug = *request.Slug
	}
	if request.Name != nil {
		sticker.Name = *request.Name
	}
	if request.Size != nil {
		sticker.Size = *request.Size
	}
	if request.Mode != nil {
		sticker.Mode = *request.Mode
	}
	if request.ImageID != nil {
		file, err := s.resolveFile(c.Request.Context(), *request.ImageID)
		if err != nil || file == nil {
			httpx.BadRequest(c, "STICKER_IMAGE_NOT_FOUND", "Image not found")
			return
		}
		sticker.Image = store.ToJSON(file)
	}
	if err := s.db.WithContext(c.Request.Context()).Save(&sticker).Error; err != nil {
		httpx.Internal(c)
		return
	}
	s.purgeSticker(c.Request.Context(), sticker, pack.Prefix)
	items := s.stickerList([]store.StickerEntity{sticker}, pack)
	httpx.OK(c, items[0])
}

func (s *service) batchRenderingSettings(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	var request batchRenderingSettingsRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), pack.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You are not a member of this publisher", http.StatusForbidden))
		return
	}
	if len(request.StickerIDs) == 0 {
		httpx.BadRequest(c, "STICKER_IDS_REQUIRED", "sticker_ids is required.")
		return
	}
	if request.Size == nil && request.Mode == nil {
		httpx.BadRequest(c, "STICKER_SIZE_OR_MODE_REQUIRED", "At least one of size or mode is required.")
		return
	}

	ids := make([]uuid.UUID, 0, len(request.StickerIDs))
	seen := map[uuid.UUID]struct{}{}
	for _, raw := range request.StickerIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if len(ids) >= maxStickersPerPack {
			break
		}
		ids = append(ids, id)
	}

	var stickers []store.StickerEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("pack_id = ? AND id IN ?", packID, ids).
		Find(&stickers).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if len(stickers) != len(ids) {
		httpx.BadRequest(c, "STICKERS_NOT_FOUND", "One or more stickers were not found in this pack.")
		return
	}
	for i := range stickers {
		if request.Size != nil {
			stickers[i].Size = *request.Size
		}
		if request.Mode != nil {
			stickers[i].Mode = *request.Mode
		}
	}
	if len(stickers) > 0 {
		if err := s.db.WithContext(c.Request.Context()).Save(&stickers).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}
	s.purgeStickers(c.Request.Context(), stickers, map[uuid.UUID]store.StickerPackEntity{pack.ID: *pack})
	httpx.OK(c, s.stickerList(stickers, pack))
}

func (s *service) deleteSticker(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), pack.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You are not a member of this publisher", http.StatusForbidden))
		return
	}
	var sticker store.StickerEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id = ? AND pack_id = ?", id, packID).First(&sticker).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if err := s.db.WithContext(c.Request.Context()).Delete(&sticker).Error; err != nil {
		httpx.Internal(c)
		return
	}
	s.purgeSticker(c.Request.Context(), sticker, pack.Prefix)
	httpx.NoContent(c)
}

func (s *service) getOwnership(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	var ownership store.StickerPackOwnershipEntity
	err = s.db.WithContext(c.Request.Context()).
		Where("pack_id = ? AND account_id = ?", packID, accountID).
		First(&ownership).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	httpx.OK(c, ownershipWire(&ownership, nil, nil, nil))
}

func (s *service) acquirePack(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	var existing store.StickerPackOwnershipEntity
	err = s.db.WithContext(c.Request.Context()).
		Where("pack_id = ? AND account_id = ?", packID, accountID).
		First(&existing).Error
	if err == nil {
		httpx.OK(c, ownershipWire(&existing, nil, nil, nil))
		return
	}
	if err != gorm.ErrRecordNotFound {
		httpx.Internal(c)
		return
	}
	ownership := store.StickerPackOwnershipEntity{
		ID:        uuid.New(),
		PackID:    pack.ID,
		AccountID: accountID,
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&ownership).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, ownershipWire(&ownership, nil, nil, nil))
}

func (s *service) releasePack(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	var ownership store.StickerPackOwnershipEntity
	err = s.db.WithContext(c.Request.Context()).
		Where("pack_id = ? AND account_id = ?", packID, accountID).
		First(&ownership).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if err := s.db.WithContext(c.Request.Context()).Delete(&ownership).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (s *service) reorderOwnedPacks(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	var request reorderRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	ids := make([]uuid.UUID, 0, len(request.Items))
	for _, item := range request.Items {
		id, err := uuid.Parse(item.ID)
		if err == nil {
			ids = append(ids, id)
		}
	}
	var ownerships []store.StickerPackOwnershipEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id IN ? AND account_id = ?", ids, accountID).
		Find(&ownerships).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if len(ownerships) != len(ids) {
		httpx.BadRequest(c, "STICKERS_OWNERSHIP_NOT_FOUND", "One or more ownership IDs were not found.")
		return
	}
	orders := make(map[uuid.UUID]int, len(request.Items))
	for _, item := range request.Items {
		if id, err := uuid.Parse(item.ID); err == nil {
			orders[id] = item.Order
		}
	}
	for i := range ownerships {
		ownerships[i].Order = orders[ownerships[i].ID]
	}
	if len(ownerships) > 0 {
		if err := s.db.WithContext(c.Request.Context()).Save(&ownerships).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}
	httpx.NoContent(c)
}

func (s *service) reorderStickers(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	packID, ok := parseID(c, "packId")
	if !ok {
		return
	}
	var request reorderRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	pack, err := s.loadPack(c.Request.Context(), packID)
	if err != nil {
		if err == store.ErrNotFound {
			c.Status(http.StatusNotFound)
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), pack.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You are not a member of this publisher", http.StatusForbidden))
		return
	}
	ids := make([]uuid.UUID, 0, len(request.Items))
	for _, item := range request.Items {
		id, err := uuid.Parse(item.ID)
		if err == nil {
			ids = append(ids, id)
		}
	}
	var stickers []store.StickerEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("id IN ? AND pack_id = ?", ids, packID).
		Find(&stickers).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if len(stickers) != len(ids) {
		httpx.BadRequest(c, "STICKERS_NOT_FOUND", "One or more sticker IDs were not found in this pack.")
		return
	}
	orders := make(map[uuid.UUID]int, len(request.Items))
	for _, item := range request.Items {
		if id, err := uuid.Parse(item.ID); err == nil {
			orders[id] = item.Order
		}
	}
	for i := range stickers {
		stickers[i].Order = orders[stickers[i].ID]
	}
	if len(stickers) > 0 {
		if err := s.db.WithContext(c.Request.Context()).Save(&stickers).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}
	httpx.NoContent(c)
}

// ---- helpers --------------------------------------------------------------

func imageRef(image any) *model.CloudFileReferenceObject {
	if image == nil {
		return nil
	}
	encoded, err := json.Marshal(image)
	if err != nil {
		return nil
	}
	var ref model.CloudFileReferenceObject
	if err := json.Unmarshal(encoded, &ref); err != nil {
		return nil
	}
	return &ref
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
