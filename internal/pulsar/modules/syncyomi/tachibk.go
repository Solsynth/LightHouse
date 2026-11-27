package syncyomi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
)

const (
	syncYomiClientName = "SyncYomi"
	syncYomiProvider   = "syncyomi"
)

type parsedManga struct {
	Source       int64
	URL          string
	Title        string
	Author       string
	ThumbnailURL string
	Chapters     []parsedChapter
	History      []parsedHistory
}

type parsedChapter struct {
	URL          string
	Name         string
	Read         bool
	LastPageRead int64
	DateFetch    int64
	ReadDuration int64
}

type parsedHistory struct {
	URL          string
	LastRead     int64
	ReadDuration int64
}

// projectBackupScrobbles best-effort parses a Tachiyomi/Mihon .tachibk payload
// (gzipped or raw protobuf) and projects read chapters into Pulsar's book
// domain (catalog_items + scrobbles). Deterministic scrobble IDs ensure
// repeated syncs never create duplicate history entries.
func projectBackupScrobbles(db *gorm.DB, accountID uuid.UUID, raw []byte) ([]*model.Scrobble, error) {
	data, err := maybeGunzip(raw)
	if err != nil {
		return nil, err
	}
	mangas, err := parseBackupProtobuf(data)
	if err != nil || len(mangas) == 0 {
		return nil, err
	}

	var created []*model.Scrobble
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, manga := range mangas {
			if strings.TrimSpace(manga.Title) == "" || strings.TrimSpace(manga.URL) == "" {
				continue
			}

			historyByURL := make(map[string]parsedHistory, len(manga.History))
			for _, h := range manga.History {
				if h.URL != "" {
					historyByURL[h.URL] = h
				}
			}

			var readChapters []parsedChapter
			for _, ch := range manga.Chapters {
				if ch.Read && strings.TrimSpace(ch.URL) != "" {
					readChapters = append(readChapters, ch)
				}
			}
			if len(readChapters) == 0 {
				continue
			}

			providerKey := strconv.FormatInt(manga.Source, 10) + ":" + manga.URL
			catReq := &model.CatalogItem{
				Domain:      model.DomainBook,
				Provider:    syncYomiProvider,
				ProviderKey: providerKey,
				Title:       strings.TrimSpace(manga.Title),
			}
			if author := strings.TrimSpace(manga.Author); author != "" {
				catReq.Subtitle = &author
			}
			if thumb := strings.TrimSpace(manga.ThumbnailURL); thumb != "" {
				catReq.ArtworkURL = &thumb
			}
			catalogItem, err := store.FindOrCreateCatalogItemDB(tx, catReq)
			if err != nil {
				return err
			}

			for _, ch := range readChapters {
				scrobbleID := chapterScrobbleID(accountID, manga.Source, manga.URL, ch.URL)

				playedAt := time.Now().UTC()
				listenedSec := int(ch.ReadDuration / 1000)
				if h, ok := historyByURL[ch.URL]; ok {
					if h.LastRead > 0 {
						playedAt = time.UnixMilli(h.LastRead).UTC()
					}
					if h.ReadDuration > 0 && listenedSec == 0 {
						listenedSec = int(h.ReadDuration / 1000)
					}
				} else if ch.DateFetch > 0 {
					playedAt = time.UnixMilli(ch.DateFetch).UTC()
				}

				caption := strings.TrimSpace(ch.Name)
				metaBytes, _ := json.Marshal(map[string]any{
					"source":         "syncyomi",
					"source_id":      manga.Source,
					"manga_url":      manga.URL,
					"chapter_url":    ch.URL,
					"last_page_read": ch.LastPageRead,
				})

				row := model.Scrobble{
					ID:              scrobbleID,
					AccountID:       accountID,
					CatalogID:       &catalogItem.ID,
					Domain:          model.DomainBook,
					ClientName:      syncYomiClientName,
					Title:           catalogItem.Title,
					Subtitle:        catalogItem.Subtitle,
					ArtworkURL:      catalogItem.ArtworkURL,
					ListenedSeconds: listenedSec,
					PlayedAt:        playedAt,
					Meta:            metaBytes,
				}
				if caption != "" {
					row.Caption = &caption
				}

				res := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "id"}},
					DoNothing: true,
				}).Create(&row)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected > 0 {
					copied := row
					created = append(created, &copied)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func chapterScrobbleID(accountID uuid.UUID, sourceID int64, mangaURL, chapterURL string) uuid.UUID {
	key := fmt.Sprintf("pulsar:syncyomi:%s:%d:%s:%s", accountID.String(), sourceID, mangaURL, chapterURL)
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(key))
}

func maybeGunzip(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		r, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, 128<<20))
	}
	return raw, nil
}

func parseBackupProtobuf(b []byte) ([]parsedManga, error) {
	var mangas []parsedManga
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			b = b[m:]
			manga, err := parseBackupManga(v)
			if err == nil {
				mangas = append(mangas, manga)
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return nil, protowire.ParseError(m)
		}
		b = b[m:]
	}
	return mangas, nil
}

func parseBackupManga(b []byte) (parsedManga, error) {
	var out parsedManga
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return out, protowire.ParseError(n)
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.Source = int64(v)
			b = b[m:]
		case num == 2 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.URL = v
			b = b[m:]
		case num == 3 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.Title = v
			b = b[m:]
		case num == 5 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.Author = v
			b = b[m:]
		case num == 9 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.ThumbnailURL = v
			b = b[m:]
		case num == 16 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			if ch, err := parseBackupChapter(v); err == nil {
				out.Chapters = append(out.Chapters, ch)
			}
			b = b[m:]
		case num == 19 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			if h, err := parseBackupHistory(v); err == nil {
				out.History = append(out.History, h)
			}
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			b = b[m:]
		}
	}
	return out, nil
}

func parseBackupChapter(b []byte) (parsedChapter, error) {
	var out parsedChapter
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return out, protowire.ParseError(n)
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.URL = v
			b = b[m:]
		case num == 2 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.Name = v
			b = b[m:]
		case num == 4 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.Read = v != 0
			b = b[m:]
		case num == 6 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.LastPageRead = int64(v)
			b = b[m:]
		case num == 7 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.DateFetch = int64(v)
			b = b[m:]
		case num == 11 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.ReadDuration = int64(v)
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			b = b[m:]
		}
	}
	return out, nil
}

func parseBackupHistory(b []byte) (parsedHistory, error) {
	var out parsedHistory
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return out, protowire.ParseError(n)
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.URL = v
			b = b[m:]
		case num == 2 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.LastRead = int64(v)
			b = b[m:]
		case num == 3 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			out.ReadDuration = int64(v)
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			b = b[m:]
		}
	}
	return out, nil
}
