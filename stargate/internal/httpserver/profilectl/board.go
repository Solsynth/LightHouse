package profilectl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
)

// registerBoard mounts the board routes on the /accounts/me group.
func registerBoard(me *gin.RouterGroup, d Deps) {
	me.GET("/board", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.AccountsProfileBoardManage), d.getBoard)
	me.PUT("/board", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.AccountsProfileBoardManage), d.replaceBoard)
}

// boardItemRequest mirrors Passport's BoardItemRequest.
type boardItemRequest struct {
	Id                 *string             `json:"id"`
	Order              int                 `json:"order"`
	Kind               model.BoardItemKind `json:"kind"`
	WidgetKey          *string             `json:"widget_key"`
	CustomAppId        *string             `json:"custom_app_id"`
	CustomAppWidgetKey *string             `json:"custom_app_widget_key"`
	IsEnabled          *bool               `json:"is_enabled"`
	Payload            map[string]any      `json:"payload"`
}

func (d Deps) getBoard(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	items, err := d.Store.ListBoardItems(c.Request.Context(), accountIDOf(user))
	if err != nil {
		internalError(c, err)
		return
	}
	if items == nil {
		items = []model.BoardItem{}
	}
	c.JSON(http.StatusOK, items)
}

// replaceBoard ports Passport's PUT /api/accounts/me/board
// (ReplaceBoardAsync: custom-app payload preservation, widget validation via
// Develop, transaction delete + insert).
func (d Deps) replaceBoard(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	var requests []boardItemRequest
	if err := c.ShouldBindJSON(&requests); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	ctx := c.Request.Context()
	accountID := accountIDOf(user)

	items := make([]model.BoardItem, 0, len(requests))
	for _, req := range requests {
		item := model.BoardItem{
			Order:     req.Order,
			Kind:      req.Kind,
			WidgetKey: req.WidgetKey,
			IsEnabled: req.IsEnabled == nil || *req.IsEnabled,
			Payload:   req.Payload,
		}
		if req.Id != nil && *req.Id != "" {
			item.Id = *req.Id
		} else {
			item.Id = uuid.NewString()
		}
		if req.CustomAppId != nil && *req.CustomAppId != "" {
			item.CustomAppId = req.CustomAppId
		}
		if req.CustomAppWidgetKey != nil && *req.CustomAppWidgetKey != "" {
			item.CustomAppWidgetKey = req.CustomAppWidgetKey
		}
		if item.Payload == nil {
			item.Payload = map[string]any{}
		}
		items = append(items, item)
	}

	try := func() error {
		// Validation runs first in the C# (it normalizes custom widget keys
		// and clears payloads); payload preservation then re-attaches the
		// existing app-owned payloads.
		if err := d.validateBoard(ctx, items); err != nil {
			return err
		}
		if err := d.preserveCustomPayloads(ctx, accountID, items); err != nil {
			return err
		}
		_, err := d.Store.ReplaceBoardItems(ctx, accountID, items)
		return err
	}
	if err := try(); err != nil {
		var ive *invalidOperation
		if errors.As(err, &ive) {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_BOARD_REPLACE_FAILED", ive.message, http.StatusBadRequest))
			return
		}
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// invalidOperation mirrors the C# InvalidOperationException used by
// AccountBoardService validation.
type invalidOperation struct{ message string }

func (e *invalidOperation) Error() string { return e.message }

// preserveCustomPayloads mirrors ReplaceBoardAsync's payload-preservation
// pass: custom-app widget payloads are owned by the app's private API, so
// existing payloads survive a layout replace.
func (d Deps) preserveCustomPayloads(ctx context.Context, accountID uuid.UUID, items []model.BoardItem) error {
	existing, err := d.Store.ListBoardItems(ctx, accountID)
	if err != nil {
		return err
	}
	existingByID := make(map[string]model.BoardItem)
	remainingByWidget := make(map[string][]map[string]any)
	for _, item := range existing {
		if item.Kind != model.BoardItemKindApp {
			continue
		}
		if item.Id != "" {
			existingByID[item.Id] = item
		}
		key := widgetInstanceKey(item.CustomAppId, item.CustomAppWidgetKey)
		remainingByWidget[key] = append(remainingByWidget[key], item.Payload)
	}
	for i := range items {
		if items[i].Kind != model.BoardItemKindApp {
			continue
		}
		key := widgetInstanceKey(items[i].CustomAppId, items[i].CustomAppWidgetKey)
		if items[i].Id != "" {
			if byID, ok := existingByID[items[i].Id]; ok &&
				strPtrEqual(byID.CustomAppId, items[i].CustomAppId) &&
				strPtrEqualFold(byID.CustomAppWidgetKey, items[i].CustomAppWidgetKey) {
				items[i].Payload = byID.Payload
				if remaining := remainingByWidget[key]; len(remaining) > 0 {
					remainingByWidget[key] = remaining[1:]
				}
				continue
			}
		}
		if remaining := remainingByWidget[key]; len(remaining) > 0 {
			items[i].Payload = remaining[0]
			remainingByWidget[key] = remaining[1:]
		} else {
			items[i].Payload = map[string]any{}
		}
	}
	return nil
}

// validateBoard ports AccountBoardService.ValidateBoardAsync +
// ValidatePrebuiltWidget + ValidateCustomWidgetAsync.
func (d Deps) validateBoard(ctx context.Context, items []model.BoardItem) error {
	seenOrders := make(map[int]bool, len(items))
	for _, item := range items {
		if seenOrders[item.Order] {
			return &invalidOperation{message: fmt.Sprintf("Duplicate board order '%d' is not allowed.", item.Order)}
		}
		seenOrders[item.Order] = true
	}

	singletonCustomWidgets := make(map[string]bool)
	for i := range items {
		switch items[i].Kind {
		case model.BoardItemKindWidget: // Prebuilt
			items[i].CustomAppId = nil
			items[i].CustomAppWidgetKey = nil
		case model.BoardItemKindApp: // CustomApp
			if items[i].CustomAppId == nil {
				return &invalidOperation{message: "Custom app board widgets require custom_app_id."}
			}
			if items[i].CustomAppWidgetKey == nil || strings.TrimSpace(*items[i].CustomAppWidgetKey) == "" {
				return &invalidOperation{message: "Custom app board widgets require custom_app_widget_key."}
			}
			if err := d.validateCustomWidget(ctx, &items[i], singletonCustomWidgets); err != nil {
				return err
			}
		default:
			return &invalidOperation{message: fmt.Sprintf("Unsupported board item kind '%d'.", items[i].Kind)}
		}
	}
	return nil
}

func (d Deps) validateCustomWidget(ctx context.Context, item *model.BoardItem, singletonCustomWidgets map[string]bool) error {
	// Layout placement only checks that the widget exists and is usable; a
	// missing Develop client degrades by skipping the existence check.
	if d.Clients == nil || d.Clients.Develop == nil {
		return nil
	}
	resp, err := d.Clients.Develop.GetBoardWidget(ctx, &gen.DyGetBoardWidgetRequest{
		AppId:     *item.CustomAppId,
		WidgetKey: *item.CustomAppWidgetKey,
	})
	if err != nil {
		if code := status.Code(err); code == codes.NotFound || code == codes.FailedPrecondition {
			return &invalidOperation{message: status.Convert(err).Message()}
		}
		return err
	}
	if resp == nil || resp.Widget == nil {
		return &invalidOperation{message: "Board widget is not configured for this app."}
	}
	if !resp.Widget.IsEnabled {
		return &invalidOperation{message: "Board widget is disabled for this app."}
	}
	if !resp.Widget.AllowMultiple {
		key := widgetInstanceKey(item.CustomAppId, item.CustomAppWidgetKey)
		if singletonCustomWidgets[key] {
			return &invalidOperation{message: fmt.Sprintf("Custom app widget '%s:%s' can only appear once.",
				*item.CustomAppId, *item.CustomAppWidgetKey)}
		}
		singletonCustomWidgets[key] = true
	}
	item.WidgetKey = nil
	if resp.Widget.Key != "" {
		item.CustomAppWidgetKey = &resp.Widget.Key
	}
	item.Payload = map[string]any{}
	return nil
}

// widgetInstanceKey mirrors WidgetInstanceKey with the C# OrdinalIgnoreCase
// comparer (the Go map uses folded keys).
func widgetInstanceKey(customAppID, widgetKey *string) string {
	var appID, key string
	if customAppID != nil {
		appID = *customAppID
	}
	if widgetKey != nil {
		key = *widgetKey
	}
	return strings.ToLower(appID + ":" + key)
}

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func strPtrEqualFold(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return strings.EqualFold(*a, *b)
}
