package bangumisync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/bangumi"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// maxRemoteRows caps one domain's import. A library larger than this keeps
// working; only the deletion pass is skipped because a truncated page set
// cannot prove an item is absent.
const maxRemoteRows = 5000

// SyncSummary is what one reconciliation moved.
type SyncSummary struct {
	Pulled      int                     `json:"pulled"`
	Pushed      int                     `json:"pushed"`
	Removed     int                     `json:"removed"`
	Conflicts   int                     `json:"conflicts"`
	Skipped     int                     `json:"skipped"`
	Failed      int                     `json:"failed"`
	Collections []CollectionSyncSummary `json:"collections"`
}

// CollectionSyncSummary is one domain's share of a reconciliation.
type CollectionSyncSummary struct {
	Domain       string `json:"domain"`
	CollectionID string `json:"collection_id"`
	Remote       int    `json:"remote"`
	Local        int    `json:"local"`
	Pulled       int    `json:"pulled"`
	Pushed       int    `json:"pushed"`
	Removed      int    `json:"removed"`
	Conflicts    int    `json:"conflicts"`
	Skipped      int    `json:"skipped"`
	Failed       int    `json:"failed"`
}

// syncConnection reconciles every supported Bangumi subject type for one
// account. Callers hold the per-connection lock. A token the API rejects even
// though its recorded expiry looked fine is refreshed once and the pass is
// retried; POST/PATCH collection writes are idempotent.
func (i *instance) syncConnection(ctx context.Context, conn *model.ExternalSyncConnection) (SyncSummary, error) {
	summary, err := i.syncConnectionOnce(ctx, conn)
	if err == nil || !errors.Is(err, bangumi.ErrUnauthorized) {
		return summary, err
	}
	if refreshErr := i.refreshAccessToken(ctx, conn); refreshErr != nil {
		return summary, err
	}
	return i.syncConnectionOnce(ctx, conn)
}

func (i *instance) syncConnectionOnce(ctx context.Context, conn *model.ExternalSyncConnection) (SyncSummary, error) {
	summary := SyncSummary{Collections: []CollectionSyncSummary{}}
	if err := i.ensureAccessToken(ctx, conn); err != nil {
		return summary, fmt.Errorf("bangumi token: %w", err)
	}
	token, err := i.accessToken(conn)
	if err != nil {
		return summary, err
	}
	username, err := i.ensureUsername(ctx, conn, token)
	if err != nil {
		return summary, fmt.Errorf("bangumi username: %w", err)
	}
	policy := i.conflictPolicy(conn)

	for _, subjectType := range []int{
		bangumi.SubjectTypeBook,
		bangumi.SubjectTypeAnime,
		bangumi.SubjectTypeGame,
		bangumi.SubjectTypeReal,
	} {
		domain := bangumi.DomainForSubjectType(subjectType)
		if domain == "" {
			continue
		}
		stats, err := i.syncDomain(ctx, conn, token, username, domain, subjectType, policy)
		summary.Pulled += stats.Pulled
		summary.Pushed += stats.Pushed
		summary.Removed += stats.Removed
		summary.Conflicts += stats.Conflicts
		summary.Skipped += stats.Skipped
		summary.Failed += stats.Failed
		summary.Collections = append(summary.Collections, stats)
		if err != nil {
			return summary, fmt.Errorf("bangumi domain %s: %w", domain, err)
		}
	}
	return summary, nil
}

func (i *instance) syncDomain(ctx context.Context, conn *model.ExternalSyncConnection, token, username, domain string, subjectType int, policy string) (CollectionSyncSummary, error) {
	stats := CollectionSyncSummary{Domain: domain}
	collection, err := i.core.Store.EnsureSyncCollection(
		ctx, conn.AccountID, domain, provider, strconv.Itoa(subjectType),
		"Bangumi · "+bangumi.DomainLabel(domain),
	)
	if err != nil {
		return stats, err
	}
	stats.CollectionID = collection.ID.String()

	rows, complete, err := i.fetchAllCollections(ctx, token, username, subjectType)
	if err != nil {
		return stats, err
	}
	stats.Remote = len(rows)

	full, err := i.core.Store.GetCollection(ctx, collection.ID)
	if err != nil {
		return stats, err
	}
	stats.Local = len(full.Items)

	localByRemote := make(map[string]*model.CollectionItem, len(full.Items))
	for index := range full.Items {
		item := &full.Items[index]
		remoteID := remoteIDForItem(item)
		if remoteID == "" {
			stats.Skipped++
			continue
		}
		if item.SyncRemoteID == nil || strings.TrimSpace(*item.SyncRemoteID) == "" {
			item.SyncRemoteID = &remoteID
		}
		localByRemote[remoteID] = item
	}

	seen := make(map[string]bool, len(rows))
	for index := range rows {
		row := &rows[index]
		remoteID := strconv.Itoa(row.SubjectID)
		seen[remoteID] = true

		item := localByRemote[remoteID]
		if item == nil {
			if err := i.createFromRemote(ctx, collection.ID, domain, remoteID, row); err != nil {
				stats.Failed++
				if isFatal(err) {
					return stats, err
				}
				i.log.Warn("bangumi item import failed", "domain", domain, "subject_id", row.SubjectID, "error", err)
				continue
			}
			stats.Pulled++
			continue
		}
		if err := i.reconcileItem(ctx, conn, token, domain, remoteID, item, row, policy, &stats); err != nil {
			stats.Failed++
			if isFatal(err) {
				return stats, err
			}
			i.log.Warn("bangumi item sync failed", "domain", domain, "subject_id", row.SubjectID, "error", err)
		}
	}

	// A complete remote listing that no longer names a previously mirrored
	// item means the user removed it on Bangumi. A truncated listing cannot
	// prove absence, so that pass is skipped.
	if complete {
		for remoteID, item := range localByRemote {
			if seen[remoteID] {
				continue
			}
			if err := i.removeOrRecreate(ctx, conn, token, domain, remoteID, item, policy, &stats); err != nil {
				stats.Failed++
				if isFatal(err) {
					return stats, err
				}
				i.log.Warn("bangumi missing item reconcile failed", "domain", domain, "remote_id", remoteID, "error", err)
			}
		}
	}
	return stats, nil
}

func (i *instance) fetchAllCollections(ctx context.Context, token, username string, subjectType int) ([]bangumi.SubjectCollection, bool, error) {
	const pageSize = 100
	rows := make([]bangumi.SubjectCollection, 0, pageSize)
	for offset := 0; ; {
		page, err := i.client.ListCollections(ctx, token, username, subjectType, offset, pageSize)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, page.Data...)
		if len(page.Data) < pageSize {
			return rows, true, nil
		}
		if page.Total > 0 && len(rows) >= page.Total {
			return rows, true, nil
		}
		if len(rows) >= maxRemoteRows {
			return rows, false, nil
		}
		offset += len(page.Data)
	}
}

func (i *instance) reconcileItem(ctx context.Context, conn *model.ExternalSyncConnection, token, domain, remoteID string, item *model.CollectionItem, row *bangumi.SubjectCollection, policy string, stats *CollectionSyncSummary) error {
	local := bangumi.FromItem(*item)
	remote := bangumi.FromRemote(*row)
	localHash := bangumi.LocalFingerprint(local)
	remoteHash := bangumi.RemoteFingerprint(remote)

	localChanged := item.SyncLocalFingerprint != nil && *item.SyncLocalFingerprint != localHash
	remoteChanged := item.SyncRemoteFingerprint != nil && *item.SyncRemoteFingerprint != remoteHash
	firstEncounter := item.SyncLocalFingerprint == nil && item.SyncRemoteFingerprint == nil

	switch {
	case firstEncounter:
		// A pre-existing row matched by catalog identity: adopt Bangumi as
		// the source of truth once, then track edits from this baseline.
		i.pullRemote(ctx, item, remoteID, remote, stats)
	case !localChanged && !remoteChanged:
		// Agreement; nothing to write.
	case localChanged && !remoteChanged:
		if err := i.pushLocal(ctx, conn, token, domain, remoteID, item, row, stats); err != nil {
			return err
		}
	case !localChanged && remoteChanged:
		i.pullRemote(ctx, item, remoteID, remote, stats)
	default:
		stats.Conflicts++
		switch policy {
		case "local":
			if err := i.pushLocal(ctx, conn, token, domain, remoteID, item, row, stats); err != nil {
				return err
			}
		case "newest":
			if row.UpdatedAt.After(item.UpdatedAt) {
				i.pullRemote(ctx, item, remoteID, remote, stats)
			} else if err := i.pushLocal(ctx, conn, token, domain, remoteID, item, row, stats); err != nil {
				return err
			}
		default: // remote
			i.pullRemote(ctx, item, remoteID, remote, stats)
		}
	}
	return nil
}

func (i *instance) pullRemote(ctx context.Context, item *model.CollectionItem, remoteID string, remote bangumi.RemoteState, stats *CollectionSyncSummary) {
	bangumi.ApplyRemote(item, remote)
	id := remoteID
	item.SyncRemoteID = &id
	localHash := bangumi.LocalFingerprint(bangumi.FromItem(*item))
	remoteHash := bangumi.RemoteFingerprint(remote)
	item.SyncLocalFingerprint = &localHash
	item.SyncRemoteFingerprint = &remoteHash
	now := time.Now().UTC()
	item.SyncedAt = &now
	if err := i.core.Store.SaveCollectionItemSync(ctx, item); err != nil {
		i.log.Warn("bangumi pull save failed", "item_id", item.ID, "error", err)
		stats.Failed++
		return
	}
	stats.Pulled++
}

func (i *instance) pushLocal(ctx context.Context, conn *model.ExternalSyncConnection, token, domain, remoteID string, item *model.CollectionItem, row *bangumi.SubjectCollection, stats *CollectionSyncSummary) error {
	subjectID, err := strconv.Atoi(remoteID)
	if err != nil {
		return fmt.Errorf("invalid bangumi subject id %q", remoteID)
	}
	local := bangumi.FromItem(*item)
	if item.Status == nil || strings.TrimSpace(*item.Status) == "" {
		status := model.StatusPlanToWatch
		item.Status = &status
		local.Status = status
	}
	create := row == nil
	payload := bangumi.PayloadFromLocal(local, domain, create)
	if err := i.client.UpsertCollection(ctx, token, subjectID, payload, create); err != nil {
		return err
	}

	current := bangumi.RemoteState{}
	if row != nil {
		current = bangumi.FromRemote(*row)
	}
	merged := bangumi.MergeRemote(current, local, domain, create)
	id := remoteID
	item.SyncRemoteID = &id
	localHash := bangumi.LocalFingerprint(local)
	remoteHash := bangumi.RemoteFingerprint(merged)
	item.SyncLocalFingerprint = &localHash
	item.SyncRemoteFingerprint = &remoteHash
	now := time.Now().UTC()
	item.SyncedAt = &now
	if err := i.core.Store.SaveCollectionItemSync(ctx, item); err != nil {
		return err
	}
	stats.Pushed++
	return nil
}

func (i *instance) createFromRemote(ctx context.Context, collectionID uuid.UUID, domain, remoteID string, row *bangumi.SubjectCollection) error {
	subjectID := row.SubjectID
	if subjectID == 0 {
		subjectID, _ = strconv.Atoi(remoteID)
	}
	catalog, err := i.findOrCreateCatalog(ctx, domain, subjectID, row.Subject)
	if err != nil {
		return err
	}
	remote := bangumi.FromRemote(*row)
	item := &model.CollectionItem{
		CollectionID: collectionID,
		CatalogID:    &catalog.ID,
	}
	id := remoteID
	item.SyncRemoteID = &id
	bangumi.ApplyRemote(item, remote)
	localHash := bangumi.LocalFingerprint(bangumi.FromItem(*item))
	remoteHash := bangumi.RemoteFingerprint(remote)
	item.SyncLocalFingerprint = &localHash
	item.SyncRemoteFingerprint = &remoteHash
	now := time.Now().UTC()
	item.SyncedAt = &now
	return i.core.Store.AddCollectionItem(ctx, item)
}

func (i *instance) removeOrRecreate(ctx context.Context, conn *model.ExternalSyncConnection, token, domain, remoteID string, item *model.CollectionItem, policy string, stats *CollectionSyncSummary) error {
	local := bangumi.FromItem(*item)
	localHash := bangumi.LocalFingerprint(local)
	localChanged := item.SyncLocalFingerprint == nil || *item.SyncLocalFingerprint != localHash

	if !localChanged {
		if err := i.core.Store.DeleteCollectionItemByID(ctx, item.CollectionID, item.ID); err != nil {
			return err
		}
		stats.Removed++
		return nil
	}
	// The user edited a row that is no longer on Bangumi. Re-create it unless
	// the policy explicitly says remote is authoritative.
	if policy == "remote" {
		if err := i.core.Store.DeleteCollectionItemByID(ctx, item.CollectionID, item.ID); err != nil {
			return err
		}
		stats.Removed++
		return nil
	}
	return i.pushLocal(ctx, conn, token, domain, remoteID, item, nil, stats)
}

func (i *instance) findOrCreateCatalog(ctx context.Context, domain string, subjectID int, subject bangumi.SlimSubject) (*model.CatalogItem, error) {
	id := strconv.Itoa(subjectID)
	existing, err := i.core.Store.FindCatalogItemForBangumi(ctx, domain, id)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	title := strings.TrimSpace(subject.NameCN)
	if title == "" {
		title = strings.TrimSpace(subject.Name)
	}
	if title == "" {
		title = "Bangumi " + id
	}
	var subtitle *string
	if name := strings.TrimSpace(subject.Name); name != "" && name != title {
		subtitle = &name
	}
	var description *string
	if summary := strings.TrimSpace(subject.ShortSummary); summary != "" {
		description = &summary
	}
	var artwork *string
	if image := subject.Images.Best(); image != "" {
		artwork = &image
	}
	var release *string
	if date := strings.TrimSpace(subject.Date); date != "" {
		release = &date
	}
	genres := make([]string, 0, 8)
	for _, tag := range subject.Tags {
		if tag.Count <= 0 || strings.TrimSpace(tag.Name) == "" {
			continue
		}
		genres = append(genres, tag.Name)
		if len(genres) == 8 {
			break
		}
	}
	catalog := &model.CatalogItem{
		Domain:        domain,
		Slug:          "bangumi-" + id,
		URN:           fmt.Sprintf("pulsar:%s:bangumi:%s", domain, id),
		Provider:      bangumiProviderName(domain),
		ProviderKey:   id,
		Title:         title,
		Subtitle:      subtitle,
		Description:   description,
		ArtworkURL:    artwork,
		ReleaseDate:   release,
		Genres:        model.ToJSON(genres),
		ExternalIDs:   model.ToJSON(map[string]string{"bangumi": id}),
		ExternalLinks: model.ToJSON([]map[string]string{{"platform": "bangumi", "label": "Bangumi", "url": "https://bgm.tv/subject/" + id}}),
	}
	return i.core.Store.FindOrCreateCatalogItem(ctx, catalog)
}

func remoteIDForItem(item *model.CollectionItem) string {
	if item.SyncRemoteID != nil && strings.TrimSpace(*item.SyncRemoteID) != "" {
		return strings.TrimSpace(*item.SyncRemoteID)
	}
	if item.CatalogItem == nil {
		return ""
	}
	if strings.EqualFold(item.CatalogItem.Provider, "bangumi") || strings.HasPrefix(strings.ToLower(item.CatalogItem.Provider), "bangumi-") {
		if key := strings.TrimSpace(item.CatalogItem.ProviderKey); key != "" {
			return key
		}
	}
	if ids := item.CatalogItem.GetExternalIDs(); ids != nil {
		return strings.TrimSpace(ids["bangumi"])
	}
	return ""
}

func bangumiProviderName(domain string) string {
	switch domain {
	case model.DomainBook:
		return "bangumi-book"
	case model.DomainGaming:
		return "bangumi-game"
	case model.DomainVideo:
		return "bangumi-video"
	default:
		return "bangumi"
	}
}

func isFatal(err error) bool {
	if errors.Is(err, bangumi.ErrUnauthorized) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var busy bangumi.BusyError
	return errors.As(err, &busy)
}
