// Package presenceingest consumes provider-driven session commands from NATS
// and writes them into Pulsar. Stargate keeps the account connections and
// provider polling; this module is the only writer of provider presence rows.
package presenceingest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/presencecmd"
)

// Factory returns the NATS consumer descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.presence_ingest",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Capabilities:   []string{"presence.ingest"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	if i.core == nil || i.core.NATS == nil || !i.core.NATS.Enabled() {
		return nil
	}
	go func() {
		err := i.core.NATS.Consume(
			ctx,
			presencecmd.Stream,
			presencecmd.Subject,
			presencecmd.Consumer,
			i.handle,
		)
		if err != nil && i.core.Log != nil && ctx.Err() == nil {
			i.core.Log.Warn("presence ingest consumer stopped", "error", err)
		}
	}()
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) handle(raw []byte) error {
	var cmd presencecmd.Command
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return err
	}
	accountID, err := uuid.Parse(strings.TrimSpace(cmd.AccountID))
	if err != nil {
		return err
	}
	manualID := strings.TrimSpace(cmd.ManualID)
	if manualID == "" {
		manualID = "default"
	}

	switch cmd.Action {
	case presencecmd.ActionStart:
		if cmd.Spec == nil {
			return errors.New("presence ingest: start command has no spec")
		}
		return i.start(accountID, manualID, *cmd.Spec)
	case presencecmd.ActionEnd:
		return i.end(accountID, manualID)
	default:
		return errors.New("presence ingest: unknown action " + cmd.Action)
	}
}

func (i *instance) start(accountID uuid.UUID, manualID string, spec presencecmd.SessionSpec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	domain := strings.TrimSpace(spec.Type)
	if domain == "" {
		domain = model.DomainCustom
	}
	title := strings.TrimSpace(deref(spec.Title))
	if title == "" {
		title = strings.TrimSpace(deref(spec.CatalogName))
	}
	if title == "" {
		title = manualID
	}
	lease := spec.LeaseMinutes
	if lease <= 0 || lease > 60 {
		lease = 5
	}

	var catalogID *uuid.UUID
	if key := strings.TrimSpace(spec.CatalogKey); key != "" {
		catalog, err := i.core.Store.FindOrCreateCatalogItem(ctx, &model.CatalogItem{
			Domain:      domain,
			Provider:    strings.TrimSpace(spec.Provider),
			ProviderKey: key,
			Title:       title,
			Subtitle:    spec.Subtitle,
			Caption:     spec.Caption,
			ArtworkURL:  spec.LargeImage,
		})
		if err == nil && catalog != nil {
			catalogID = &catalog.ID
		}
	}

	session := &model.ActiveSession{
		AccountID:    accountID,
		CatalogID:    catalogID,
		Domain:       domain,
		ManualID:     manualID,
		Title:        title,
		Subtitle:     spec.Subtitle,
		Caption:      spec.Caption,
		ArtworkURL:   spec.LargeImage,
		LargeImage:   spec.LargeImage,
		SmallImage:   spec.SmallImage,
		LeaseMinutes: lease,
		StartedAt:    time.Now().UTC(),
		Meta:         model.ToJSON(spec.Meta),
	}
	if err := i.core.Store.UpsertActiveSession(ctx, session); err != nil {
		return err
	}
	i.core.PublishSessionUpdate(ctx, accountID, session)
	return nil
}

func (i *instance) end(accountID uuid.UUID, manualID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := i.core.Store.EndActiveSession(ctx, accountID, manualID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	i.core.PublishSessionUpdate(ctx, accountID, nil)
	return nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
