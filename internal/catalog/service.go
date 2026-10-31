package catalog

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YeandAmen/radwave/internal/model"
	"github.com/YeandAmen/radwave/internal/store"
)

type Service struct {
	Store  *store.Store
	Client *Client
	mu     sync.Mutex
	index  *Index
}

func New(s *store.Store, token string) *Service { return &Service{Store: s, Client: NewClient(token)} }

func (s *Service) Info(ctx context.Context) (Info, bool, error) {
	var info Info
	ok, err := s.Store.CatalogMeta(ctx, &info)
	return info, ok, err
}

func (s *Service) Load(ctx context.Context) (*Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok, err := s.Info(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, store.ErrNotFound
	}
	if s.index != nil && s.index.Info.Digest == info.Digest {
		return s.index, nil
	}
	data, err := s.Store.CatalogArchive(ctx, info.Digest, info.Bytes)
	if err != nil {
		return nil, err
	}
	index, err := Open(ctx, data)
	if err != nil {
		return nil, err
	}
	index.Info = info
	s.index = index
	return index, nil
}

func (s *Service) Import(ctx context.Context, data []byte, version int) (Info, error) {
	idx, err := Open(ctx, data)
	if err != nil {
		return Info{}, err
	}
	idx.Info.Version = version
	if old, ok, err := s.Info(ctx); err != nil {
		return Info{}, err
	} else if ok && old.AsOf > idx.Info.AsOf {
		return Info{}, errors.New("an older dataset cannot replace a newer snapshot")
	}
	if err := s.Store.SaveCatalog(ctx, idx.Info.Digest, idx.Info, data); err != nil {
		return Info{}, err
	}
	s.mu.Lock()
	s.index = idx
	s.mu.Unlock()
	now := time.Now().UTC()
	next := now.Add(24 * time.Hour)
	source, _, err := s.Store.AddSource(ctx, model.Source{ID: SourceID, Name: "RoleBase · Kaggle job catalogue", URL: DatasetURL, Kind: "kaggle_dataset", Enabled: s.Client.Configured(), IntervalMinutes: 1440})
	if err != nil {
		return Info{}, err
	}
	source.Status, source.LastError, source.ItemCount, source.LastCrawledAt, source.NextCrawlAt = "ok", "", idx.Info.Open, &now, &next
	if _, err := s.Store.RecordSourceFetch(ctx, source); err != nil {
		return Info{}, err
	}
	return idx.Info, nil
}

func (s *Service) Refresh(ctx context.Context) (Info, error) {
	meta, err := s.Client.Metadata(ctx)
	if err != nil {
		return Info{}, err
	}
	info, exists, err := s.Info(ctx)
	if err != nil {
		return Info{}, err
	}
	if exists && info.Version == meta.Version {
		return info, nil
	}
	data, err := s.Client.Download(ctx, meta.Version)
	if err != nil {
		return Info{}, err
	}
	return s.Import(ctx, data, meta.Version)
}

func relevant(q model.Query) bool {
	return (q.Platform == "" || q.Platform == "kaggle") && (q.Source == "" || q.Source == SourceID) && (q.Kind == "" || strings.Contains(q.Kind, "job") || strings.Contains(q.Kind, "internship"))
}

func (s *Service) List(ctx context.Context, q model.Query) ([]model.Opportunity, int, error) {
	baseQ := q
	baseQ.Offset = 0
	baseQ.Limit = 10000
	base, baseTotal, err := s.Store.List(ctx, baseQ)
	if err != nil {
		return nil, 0, err
	}
	filtered := base[:0]
	exclude := map[string]bool{}
	for _, o := range base {
		exclude[o.URL] = true
		if Match(o, q) {
			filtered = append(filtered, o)
		}
	}
	base = filtered
	if q.JobField != "" || q.JobLevel != "" || q.RemoteOnly || q.FresherOnly || q.Skill != "" || q.MaxYears != nil || q.MaxRisk != nil {
		baseTotal = len(base)
	}
	sort.SliceStable(base, func(i, j int) bool {
		a, b := base[i], base[j]
		if q.Sort == "deadline" || q.Sort == "" {
			if a.Deadline != "" && b.Deadline == "" {
				return true
			}
			if a.Deadline == "" && b.Deadline != "" {
				return false
			}
			if a.Deadline != b.Deadline {
				return a.Deadline < b.Deadline
			}
		}
		if q.Sort == "popular" {
			var av, bv int64 = -1, -1
			if a.Reactions != nil {
				av = *a.Reactions
			}
			if b.Reactions != nil {
				bv = *b.Reactions
			}
			if av != bv {
				return av > bv
			}
		}
		ad, bd := a.FirstSeenAt, b.FirstSeenAt
		if a.PublishedAt != nil {
			ad = *a.PublishedAt
		}
		if b.PublishedAt != nil {
			bd = *b.PublishedAt
		}
		if q.Sort == "updated" {
			ad, bd = a.UpdatedAt, b.UpdatedAt
		}
		return ad.After(bd)
	})
	if relevant(q) {
		if _, ok, err := s.Info(ctx); err != nil {
			return nil, 0, err
		} else if ok {
			idx, err := s.Load(ctx)
			if err != nil {
				return nil, 0, err
			}
			items := []model.Opportunity{}
			position, nextBase := 0, 0
			limit := q.Limit
			if limit <= 0 {
				limit = 48
			}
			limit = min(limit, 10000)
			emitBase := func(o model.Opportunity) {
				if position >= q.Offset && len(items) < limit {
					items = append(items, o)
				}
				position++
			}
			total, err := idx.walk(ctx, q, exclude, func(x record, _ int) {
				d := x.posted
				if d == 0 {
					d = x.seen
				}
				rank := time.Unix(int64(d)*86400, 0).UTC()
				if q.Sort == "updated" {
					rank, _ = time.Parse("2006-01-02", idx.Info.AsOf)
				}
				for nextBase < len(base) {
					o := base[nextBase]
					before := (q.Sort == "deadline" || q.Sort == "") && o.Deadline != ""
					if q.Sort == "popular" && o.Reactions != nil {
						before = true
					}
					od := o.FirstSeenAt
					if o.PublishedAt != nil {
						od = *o.PublishedAt
					}
					if q.Sort == "updated" {
						od = o.UpdatedAt
					}
					if !before && !od.After(rank) {
						break
					}
					emitBase(o)
					nextBase++
				}
				if position >= q.Offset && len(items) < limit {
					items = append(items, idx.Opportunity(x))
				}
				position++
			})
			if err != nil {
				return nil, 0, err
			}
			for nextBase < len(base) {
				emitBase(base[nextBase])
				nextBase++
			}
			return items, baseTotal + total, nil
		}
	}
	start := min(max(0, q.Offset), len(base))
	limit := q.Limit
	if limit <= 0 {
		limit = 48
	}
	end := min(len(base), start+min(10000, limit))
	return base[start:end], baseTotal, nil
}

func (s *Service) Get(ctx context.Context, id string) (model.Opportunity, []model.Observation, error) {
	if !strings.HasPrefix(id, "kaggle-") {
		o, err := s.Store.Get(ctx, id)
		if err != nil {
			return o, nil, err
		}
		h, err := s.Store.History(ctx, id)
		return o, h, err
	}
	idx, err := s.Load(ctx)
	if err != nil {
		return model.Opportunity{}, nil, err
	}
	o, err := idx.Get(id)
	if err != nil {
		return o, nil, store.ErrNotFound
	}
	return o, []model.Observation{{OpportunityID: o.ID, ObservedAt: o.LastFetchedAt, CaptureMethod: o.CaptureMethod, Changes: []string{"Dataset snapshot · " + idx.Info.AsOf}}}, nil
}
