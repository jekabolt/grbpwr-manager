package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// RefreshHero re-resolves the stored hero (Insert form) and refreshes the cache.
// With Insert-form storage there is no reverse flatten: we just read what the
// admin stored and re-run resolution (picking up e.g. media/product changes).
func (s *Store) RefreshHero(ctx context.Context) error {
	//TODO: update categories count
	hfi, legacy, err := s.getHeroInsert(ctx)
	if err != nil {
		return fmt.Errorf("failed to get hero insert: %w", err)
	}
	if legacy {
		// Never overwrite a legacy row from an incidental refresh — that would
		// destroy the admin's configured hero. It is replaced only by a
		// deliberate admin SetHero (AddHero).
		slog.WarnContext(ctx, "skipping RefreshHero: hero is in legacy format; not overwriting to preserve data")
		return nil
	}
	if hfi == nil {
		return nil // nothing stored yet
	}
	if err := s.SetHero(ctx, *hfi); err != nil {
		return fmt.Errorf("failed to set hero: %w", err)
	}
	return nil
}

func isValidHeroType(t entity.HeroType) bool {
	return t >= entity.HeroTypeSingle && t <= entity.HeroTypeLookbook
}

// SetHero persists the hero in its Insert form (the source of truth) and
// refreshes the resolved cache in the same transaction.
func (s *Store) SetHero(ctx context.Context, hfi entity.HeroFullInsert) error {
	for _, e := range hfi.Entities {
		if !isValidHeroType(e.Type) {
			return fmt.Errorf("invalid hero type: %d", e.Type)
		}
	}

	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := deleteExistingHeroData(ctx, rep); err != nil {
			return fmt.Errorf("failed to delete hero data: %w", err)
		}

		if err := insertHeroInsert(ctx, rep, hfi); err != nil {
			return fmt.Errorf("failed to insert hero data: %w", err)
		}

		// Resolve once for the cache (frontend reads the resolved form from cache).
		heroFull, err := buildHeroData(ctx, rep, hfi)
		if err != nil {
			return fmt.Errorf("failed to build hero data: %w", err)
		}
		cache.UpdateHero(heroFull)

		return nil
	})
}

// GetHero reads the stored Insert form and resolves it into the read shape.
// Resolution needs cross-store access (media/products/archive), so it runs
// inside a (read) transaction to obtain a repository handle.
func (s *Store) GetHero(ctx context.Context) (*entity.HeroFullWithTranslations, error) {
	hfi, legacy, err := s.getHeroInsert(ctx)
	if err != nil {
		return nil, err
	}
	if legacy {
		slog.WarnContext(ctx, "hero is in legacy (pre-L2) format; serving empty until re-saved from admin (legacy data preserved)")
		return &entity.HeroFullWithTranslations{}, nil
	}
	if hfi == nil {
		return &entity.HeroFullWithTranslations{}, nil
	}

	var heroFull *entity.HeroFullWithTranslations
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		heroFull, err = buildHeroData(ctx, rep, *hfi)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve hero: %w", err)
	}
	return heroFull, nil
}

// heroSchemaVersion identifies the persisted hero payload shape. Bump it when
// the stored HeroFullInsert JSON changes incompatibly. Rows without a matching
// version are treated as legacy and are never auto-overwritten.
const heroSchemaVersion = 2

// storedHero is the on-disk envelope: a schema version plus the Insert-form hero.
// Pre-L2 rows have no envelope (they stored the resolved shape at the top level),
// so they decode with SchemaVersion == 0 and are recognised as legacy.
type storedHero struct {
	SchemaVersion int                   `json:"schema_version"`
	Hero          entity.HeroFullInsert `json:"hero"`
}

// getHeroInsert reads the stored hero envelope. It returns (insert, legacy, err):
//   - legacy == true means the row predates L2 Insert-form storage (resolved
//     shape, no schema version). Such rows are NOT auto-migratable and MUST NOT
//     be overwritten by an incidental RefreshHero — only a deliberate admin
//     SetHero replaces them. Callers should serve an empty hero in that case.
//   - a genuine JSON error is propagated (fail fast), not silently swallowed.
func (s *Store) getHeroInsert(ctx context.Context) (*entity.HeroFullInsert, bool, error) {
	query := `SELECT data FROM hero`

	type heroRow struct {
		Data []byte `db:"data"`
	}

	heroRaw, err := storeutil.QueryNamedOne[heroRow](ctx, s.DB, query, nil)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to get hero: %w", err)
	}

	var env storedHero
	if err := json.Unmarshal(heroRaw.Data, &env); err != nil {
		return nil, false, fmt.Errorf("failed to unmarshal hero data: %w", err)
	}
	if env.SchemaVersion < heroSchemaVersion {
		// Legacy (pre-L2) resolved-shape row: preserve it, do not overwrite.
		return nil, true, nil
	}
	return &env.Hero, false, nil
}

func deleteExistingHeroData(ctx context.Context, rep dependency.Repository) error {
	query := `DELETE FROM hero`
	if _, err := rep.DB().ExecContext(ctx, query); err != nil {
		return fmt.Errorf("failed to delete hero data: %w", err)
	}
	return nil
}

func insertHeroInsert(ctx context.Context, rep dependency.Repository, hfi entity.HeroFullInsert) error {
	jsonData, err := json.Marshal(storedHero{SchemaVersion: heroSchemaVersion, Hero: hfi})
	if err != nil {
		return fmt.Errorf("failed to marshal hero insert: %w", err)
	}
	query := `INSERT INTO hero (data) VALUES (:data)`
	if _, err := rep.DB().NamedExecContext(ctx, query, map[string]any{"data": jsonData}); err != nil {
		return fmt.Errorf("failed to insert hero data: %w", err)
	}
	return nil
}

// ─── resolvers (shared across block types) ──────────────────────────────────

// heroMediaEmpty reports whether a media slot has no image referenced at all.
func heroMediaEmpty(m entity.HeroMedia) bool {
	return m.PortraitId == 0 && m.LandscapeId == 0
}

// resolveHeroMedia turns a HeroMedia (ids) into a HeroMediaFull. A missing
// portrait/landscape id falls back to the other, matching the prior behaviour.
func resolveHeroMedia(ctx context.Context, rep dependency.Repository, m entity.HeroMedia) (entity.HeroMediaFull, error) {
	portraitId, landscapeId := m.PortraitId, m.LandscapeId
	if portraitId == 0 {
		portraitId = landscapeId
	}
	if landscapeId == 0 {
		landscapeId = portraitId
	}

	full := entity.HeroMediaFull{DisableOverlay: m.DisableOverlay}
	if portraitId != 0 {
		pm, err := rep.Media().GetMediaById(ctx, portraitId)
		if err != nil {
			return full, fmt.Errorf("failed to get portrait media %d: %w", portraitId, err)
		}
		full.Portrait = *pm
	}
	if landscapeId != 0 {
		lm, err := rep.Media().GetMediaById(ctx, landscapeId)
		if err != nil {
			return full, fmt.Errorf("failed to get landscape media %d: %w", landscapeId, err)
		}
		full.Landscape = *lm
	}
	return full, nil
}

func resolveHeroSingle(ctx context.Context, rep dependency.Repository, s entity.HeroSingleInsert) (entity.HeroSingleWithTranslations, error) {
	media, err := resolveHeroMedia(ctx, rep, s.Media)
	if err != nil {
		return entity.HeroSingleWithTranslations{}, err
	}
	return entity.HeroSingleWithTranslations{
		Media:        media,
		ExploreLink:  s.ExploreLink,
		Translations: s.Translations,
	}, nil
}

func resolveHeroSingleSlice(ctx context.Context, rep dependency.Repository, in []entity.HeroSingleInsert) ([]entity.HeroSingleWithTranslations, error) {
	out := make([]entity.HeroSingleWithTranslations, 0, len(in))
	for i := range in {
		s, err := resolveHeroSingle(ctx, rep, in[i])
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func filterVisibleProducts(products []entity.Product) []entity.Product {
	prds := make([]entity.Product, 0, len(products))
	for _, p := range products {
		if p.ProductDisplay.ProductBody.ProductBodyInsert.Hidden.Valid && p.ProductDisplay.ProductBody.ProductBodyInsert.Hidden.Bool {
			continue
		}
		prds = append(prds, p)
	}
	return prds
}

func buildHeroData(ctx context.Context, rep dependency.Repository, hfi entity.HeroFullInsert) (*entity.HeroFullWithTranslations, error) {
	entities := make([]entity.HeroEntityWithTranslations, 0, len(hfi.Entities))
	for n, e := range hfi.Entities {
		before := len(entities)
		switch e.Type {
		case entity.HeroTypeSingle:
			if heroMediaEmpty(e.Single.Media) {
				continue
			}
			single, err := resolveHeroSingle(ctx, rep, e.Single)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve single hero, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{Type: e.Type, Single: &single})

		case entity.HeroTypeDouble:
			if heroMediaEmpty(e.Double.Left.Media) || heroMediaEmpty(e.Double.Right.Media) {
				continue
			}
			left, err := resolveHeroSingle(ctx, rep, e.Double.Left)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve double hero left, skipping", slog.String("err", err.Error()))
				continue
			}
			right, err := resolveHeroSingle(ctx, rep, e.Double.Right)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve double hero right, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type:   e.Type,
				Double: &entity.HeroDoubleWithTranslations{Left: left, Right: right},
			})

		case entity.HeroTypeMain:
			// main is only allowed at the first position
			if n != 0 {
				continue
			}
			if heroMediaEmpty(e.Main.Media) {
				continue
			}
			media, err := resolveHeroMedia(ctx, rep, e.Main.Media)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve main hero, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Main: &entity.HeroMainWithTranslations{
					Media:        media,
					ExploreLink:  e.Main.ExploreLink,
					Translations: e.Main.Translations,
				},
			})

		case entity.HeroTypeFeaturedProducts:
			if len(e.FeaturedProducts.ProductIDs) == 0 {
				slog.ErrorContext(ctx, "no product ids provided for featured products, skipping")
				continue
			}
			products, err := rep.Products().GetProductsByIds(ctx, e.FeaturedProducts.ProductIDs)
			if err != nil {
				slog.ErrorContext(ctx, "failed to get featured products, skipping", slog.String("err", err.Error()))
				continue
			}
			prds := filterVisibleProducts(products)
			if len(prds) == 0 {
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				FeaturedProducts: &entity.HeroFeaturedProductsWithTranslations{
					Products:     prds,
					ExploreLink:  e.FeaturedProducts.ExploreLink,
					Translations: e.FeaturedProducts.Translations,
				},
			})

		case entity.HeroTypeFeaturedProductsTag:
			if e.FeaturedProductsTag.Tag == "" {
				continue
			}
			products, err := rep.Products().GetProductsByTag(ctx, e.FeaturedProductsTag.Tag)
			if err != nil {
				slog.ErrorContext(ctx, "failed to get products by tag, skipping", slog.String("err", err.Error()))
				continue
			}
			prds := filterVisibleProducts(products)
			if len(prds) == 0 {
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				FeaturedProductsTag: &entity.HeroFeaturedProductsTagWithTranslations{
					Tag: e.FeaturedProductsTag.Tag,
					Products: entity.HeroFeaturedProductsWithTranslations{
						Products:     prds,
						ExploreLink:  "",  // tag-based products carry no explore link
						Translations: nil, // outer block carries the translations
					},
					Translations: e.FeaturedProductsTag.Translations,
				},
			})

		case entity.HeroTypeFeaturedArchive:
			archive, err := rep.Archive().GetArchiveById(ctx, e.FeaturedArchive.ArchiveId)
			if err != nil {
				slog.ErrorContext(ctx, "failed to get archive by id, skipping",
					slog.String("err", err.Error()),
					slog.Int("archive_id", e.FeaturedArchive.ArchiveId))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				FeaturedArchive: &entity.HeroFeaturedArchiveWithTranslations{
					Archive:      *archive,
					Tag:          e.FeaturedArchive.Tag,
					Translations: e.FeaturedArchive.Translations,
				},
			})

		case entity.HeroTypeEmbed:
			fallback, err := resolveHeroMedia(ctx, rep, e.Embed.Fallback)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve embed fallback, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Embed: &entity.HeroEmbedWithTranslations{
					EmbedUrl:     e.Embed.EmbedUrl,
					Fallback:     fallback,
					CtaLink:      e.Embed.CtaLink,
					Translations: e.Embed.Translations,
				},
			})

		case entity.HeroTypeDrop:
			media, err := resolveHeroMedia(ctx, rep, e.Drop.Media)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve drop media, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Drop: &entity.HeroDropWithTranslations{
					Media:        media,
					ReleaseAt:    e.Drop.ReleaseAt,
					ExploreLink:  e.Drop.ExploreLink,
					Tag:          e.Drop.Tag,
					Translations: e.Drop.Translations,
				},
			})

		case entity.HeroTypeLastChance:
			products, err := rep.Products().GetLowStockProducts(ctx, e.LastChance.StockThreshold, e.LastChance.Limit)
			if err != nil {
				slog.ErrorContext(ctx, "failed to get low stock products, skipping", slog.String("err", err.Error()))
				continue
			}
			prds := filterVisibleProducts(products)
			if len(prds) == 0 {
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				LastChance: &entity.HeroLastChanceWithTranslations{
					Products:     prds,
					ExploreLink:  e.LastChance.ExploreLink,
					Translations: e.LastChance.Translations,
				},
			})

		case entity.HeroTypeMarquee:
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Marquee: &entity.HeroMarqueeWithTranslations{
					Link:         e.Marquee.Link,
					Speed:        e.Marquee.Speed,
					Translations: e.Marquee.Translations,
				},
			})

		case entity.HeroTypeNewArrivals:
			limit := e.NewArrivals.Limit
			if limit <= 0 {
				limit = 8
			}
			products, _, err := rep.Products().GetProductsPaged(ctx, limit, 0, []entity.SortFactor{entity.CreatedAt}, entity.Descending, nil, false)
			if err != nil {
				slog.ErrorContext(ctx, "failed to get newest products, skipping", slog.String("err", err.Error()))
				continue
			}
			prds := filterVisibleProducts(products)
			if len(prds) == 0 {
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				NewArrivals: &entity.HeroNewArrivalsWithTranslations{
					Products:     prds,
					ExploreLink:  e.NewArrivals.ExploreLink,
					Translations: e.NewArrivals.Translations,
				},
			})

		case entity.HeroTypeSlideshow:
			slides, err := resolveHeroSingleSlice(ctx, rep, e.Slideshow.Slides)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve slideshow slides, skipping", slog.String("err", err.Error()))
				continue
			}
			if len(slides) == 0 {
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Slideshow: &entity.HeroSlideshowWithTranslations{
					Slides:     slides,
					IntervalMs: e.Slideshow.IntervalMs,
				},
			})

		case entity.HeroTypeMosaic:
			tiles, err := resolveHeroSingleSlice(ctx, rep, e.Mosaic.Tiles)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve mosaic tiles, skipping", slog.String("err", err.Error()))
				continue
			}
			if len(tiles) == 0 {
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Mosaic: &entity.HeroMosaicWithTranslations{
					Tiles:   tiles,
					Columns: e.Mosaic.Columns,
				},
			})

		case entity.HeroTypeSplit:
			media, err := resolveHeroSingle(ctx, rep, e.Split.Media)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve split media, skipping", slog.String("err", err.Error()))
				continue
			}
			var prds []entity.Product
			if len(e.Split.ProductIDs) > 0 {
				products, err := rep.Products().GetProductsByIds(ctx, e.Split.ProductIDs)
				if err != nil {
					slog.ErrorContext(ctx, "failed to get split products, showing media only", slog.String("err", err.Error()))
				} else {
					prds = filterVisibleProducts(products)
				}
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Split: &entity.HeroSplitWithTranslations{
					Media:     media,
					Products:  prds,
					MediaLeft: e.Split.MediaLeft,
				},
			})

		case entity.HeroTypeVideo:
			if e.Video.MediaId == 0 {
				slog.ErrorContext(ctx, "video hero has no media id, skipping")
				continue
			}
			videoMedia, err := rep.Media().GetMediaById(ctx, e.Video.MediaId)
			if err != nil {
				slog.ErrorContext(ctx, "failed to get video media, skipping", slog.String("err", err.Error()))
				continue
			}
			video := &entity.HeroVideoWithTranslations{
				Media:        *videoMedia,
				Autoplay:     e.Video.Autoplay,
				Loop:         e.Video.Loop,
				Muted:        e.Video.Muted,
				CtaLink:      e.Video.CtaLink,
				Translations: e.Video.Translations,
			}
			if e.Video.PosterMediaId != 0 {
				poster, err := rep.Media().GetMediaById(ctx, e.Video.PosterMediaId)
				if err != nil {
					slog.ErrorContext(ctx, "failed to get video poster media", slog.String("err", err.Error()))
				} else {
					video.PosterMedia = *poster
				}
			}
			entities = append(entities, entity.HeroEntityWithTranslations{Type: e.Type, Video: video})

		case entity.HeroTypeProductSpotlight:
			if e.ProductSpotlight.ProductId == 0 {
				slog.ErrorContext(ctx, "product spotlight has no product id, skipping")
				continue
			}
			products, err := rep.Products().GetProductsByIds(ctx, []int{e.ProductSpotlight.ProductId})
			if err != nil {
				slog.ErrorContext(ctx, "failed to get spotlight product, skipping", slog.String("err", err.Error()))
				continue
			}
			prds := filterVisibleProducts(products)
			if len(prds) == 0 {
				continue
			}
			media, err := resolveHeroMedia(ctx, rep, e.ProductSpotlight.Media)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve spotlight media, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				ProductSpotlight: &entity.HeroProductSpotlightWithTranslations{
					Product:      prds[0],
					Media:        media,
					ExploreLink:  e.ProductSpotlight.ExploreLink,
					Translations: e.ProductSpotlight.Translations,
				},
			})

		case entity.HeroTypeNewsletter:
			media, err := resolveHeroMedia(ctx, rep, e.Newsletter.Media)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve newsletter media, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Newsletter: &entity.HeroNewsletterWithTranslations{
					Media:        media,
					Translations: e.Newsletter.Translations,
				},
			})

		case entity.HeroTypeStatement:
			media, err := resolveHeroMedia(ctx, rep, e.Statement.Media)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve statement media, skipping", slog.String("err", err.Error()))
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Statement: &entity.HeroStatementWithTranslations{
					Media:        media,
					ExploreLink:  e.Statement.ExploreLink,
					Translations: e.Statement.Translations,
				},
			})

		case entity.HeroTypeLookbook:
			frames, err := resolveHeroSingleSlice(ctx, rep, e.Lookbook.Frames)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve lookbook frames, skipping", slog.String("err", err.Error()))
				continue
			}
			if len(frames) == 0 {
				continue
			}
			entities = append(entities, entity.HeroEntityWithTranslations{
				Type: e.Type,
				Lookbook: &entity.HeroLookbookWithTranslations{
					Frames:       frames,
					ExploreLink:  e.Lookbook.ExploreLink,
					Translations: e.Lookbook.Translations,
				},
			})
		}

		// carry the cross-cutting modifiers onto whatever block this iteration
		// appended (works for every type, old and new)
		if len(entities) > before {
			entities[len(entities)-1].Audience = e.Audience
			entities[len(entities)-1].MinTierId = e.MinTierId
		}
	}

	heroFull := entity.HeroFullWithTranslations{
		Entities: entities,
		NavFeatured: entity.NavFeaturedWithTranslations{
			Men: entity.NavFeaturedEntityWithTranslations{
				FeaturedTag:       hfi.NavFeatured.Men.FeaturedTag,
				FeaturedArchiveId: strconv.Itoa(hfi.NavFeatured.Men.FeaturedArchiveId),
				Translations:      hfi.NavFeatured.Men.Translations,
			},
			Women: entity.NavFeaturedEntityWithTranslations{
				FeaturedTag:       hfi.NavFeatured.Women.FeaturedTag,
				FeaturedArchiveId: strconv.Itoa(hfi.NavFeatured.Women.FeaturedArchiveId),
				Translations:      hfi.NavFeatured.Women.Translations,
			},
		},
	}

	if hfi.NavFeatured.Men.MediaId != 0 {
		menMedia, err := rep.Media().GetMediaById(ctx, hfi.NavFeatured.Men.MediaId)
		if err != nil {
			slog.ErrorContext(ctx, "failed to get nav men media",
				slog.String("err", err.Error()),
				slog.Int("media_id", hfi.NavFeatured.Men.MediaId))
		} else {
			heroFull.NavFeatured.Men.Media = *menMedia
		}
	}

	if hfi.NavFeatured.Women.MediaId != 0 {
		womenMedia, err := rep.Media().GetMediaById(ctx, hfi.NavFeatured.Women.MediaId)
		if err != nil {
			slog.ErrorContext(ctx, "failed to get nav women media",
				slog.String("err", err.Error()),
				slog.Int("media_id", hfi.NavFeatured.Women.MediaId))
		} else {
			heroFull.NavFeatured.Women.Media = *womenMedia
		}
	}

	return &heroFull, nil
}
