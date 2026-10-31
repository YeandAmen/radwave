// Package catalog keeps the complete daily Kaggle dataset in a compact,
// immutable search index. Only requested results become opportunity records.
package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YeandAmen/radwave/internal/ingest"
	"github.com/YeandAmen/radwave/internal/model"
)

const DatasetURL = "https://www.kaggle.com/datasets/chandranshbinjola/job-listings"
const SourceID = "source-kaggle-jobs"

type Info struct {
	Dataset    string              `json:"dataset"`
	AsOf       string              `json:"as_of"`
	Version    int                 `json:"version"`
	Digest     string              `json:"digest"`
	Bytes      int                 `json:"compressed_bytes"`
	Rows       int                 `json:"rows"`
	Open       int                 `json:"open"`
	Closed     int                 `json:"closed"`
	Rejected   int                 `json:"rejected"`
	Duplicates int                 `json:"duplicates"`
	Remote     int                 `json:"remote"`
	Fresher    int                 `json:"fresher_friendly"`
	Pay        int                 `json:"with_pay"`
	Kinds      map[string]int      `json:"kinds"`
	Fields     map[string]int      `json:"fields"`
	Levels     map[string]int      `json:"levels"`
	Daily      []map[string]string `json:"daily"`
	Companies  []map[string]string `json:"employers"`
	Skills     []map[string]string `json:"skill_demand"`
}

type record struct {
	id                                                                                            [12]byte
	company, title, url, location, skills, team, field, level, employment, ats, currency, reasons string
	search                                                                                        string
	posted, seen, closed                                                                          int32
	payMin, payMax                                                                                float64
	openings                                                                                      int32
	years, risk                                                                                   int16
	remote, fresher                                                                               int8
}

type Index struct {
	Info   Info
	rows   []record
	byID   map[[12]byte]int
	newest []int
}

func date(raw string) int32 {
	if raw == "" {
		return 0
	}
	d, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return -1
	}
	return int32(d.Unix() / 86400)
}
func dateText(day int32) string {
	if day <= 0 {
		return ""
	}
	return time.Unix(int64(day)*86400, 0).UTC().Format("2006-01-02")
}
func nullableInt(raw string) int16 {
	if raw == "" {
		return -1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 32767 {
		return -1
	}
	return int16(n)
}
func flag(raw string) int8 {
	if raw == "" {
		return -1
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return -1
	}
	if b {
		return 1
	}
	return 0
}

func Open(ctx context.Context, data []byte) (*Index, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("the dataset is not a readable ZIP archive")
	}
	idx := &Index{Info: Info{Dataset: DatasetURL, Bytes: len(data), Kinds: map[string]int{}, Fields: map[string]int{}, Levels: map[string]int{}}, byID: map[[12]byte]int{}}
	hash := sha256.Sum256(data)
	idx.Info.Digest = hex.EncodeToString(hash[:])
	var jobs *zip.File
	for _, file := range z.File {
		if file.Name == "jobs.csv" {
			jobs = file
		}
		if file.UncompressedSize64 > 400<<20 {
			return nil, errors.New("dataset member exceeds the supported 400 MB limit")
		}
		if file.Name == "open_jobs_daily.csv" || file.Name == "jobs_by_company.csv" || file.Name == "skill_demand_daily.csv" {
			rows, err := table(file)
			if err != nil {
				return nil, err
			}
			switch file.Name {
			case "open_jobs_daily.csv":
				idx.Info.Daily = rows
				for _, r := range rows {
					if r["date"] > idx.Info.AsOf {
						idx.Info.AsOf = r["date"]
					}
				}
			case "jobs_by_company.csv":
				idx.Info.Companies = rows
			case "skill_demand_daily.csv":
				idx.Info.Skills = rows
			}
		}
	}
	if jobs == nil {
		return nil, errors.New("the dataset ZIP must contain jobs.csv")
	}
	estimate := min(1500000, int(jobs.UncompressedSize64)/220+1)
	idx.rows = make([]record, 0, estimate)
	r, err := jobs.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	c := csv.NewReader(io.LimitReader(r, 400<<20))
	c.ReuseRecord = true
	header, err := c.Read()
	if err != nil {
		return nil, err
	}
	columns := map[string]int{}
	for i, name := range header {
		columns[strings.TrimPrefix(name, "\ufeff")] = i
	}
	for _, name := range []string{"source", "company", "title", "url", "category", "level", "remote", "posted", "first_seen", "closed_at", "fresher_friendly"} {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("jobs.csv is missing %s", name)
		}
	}
	get := func(row []string, name string) string {
		if col, ok := columns[name]; ok && col < len(row) {
			return strings.TrimSpace(row[col])
		}
		return ""
	}
	for {
		row, err := c.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("jobs.csv contains a malformed CSV record")
		}
		idx.Info.Rows++
		if idx.Info.Rows%10000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		rawURL := get(row, "url")
		if _, err := ingest.ValidateURL(rawURL); err != nil {
			idx.Info.Rejected++
			continue
		}
		x := record{company: get(row, "company"), title: get(row, "title"), url: ingest.CanonicalURL(rawURL), location: get(row, "location"), skills: get(row, "skills"), team: get(row, "team"), field: get(row, "category"), level: get(row, "level"), employment: get(row, "employment"), ats: get(row, "source"), currency: get(row, "pay_currency"), posted: date(get(row, "posted")), seen: date(get(row, "first_seen")), closed: date(get(row, "closed_at")), years: nullableInt(get(row, "years_experience")), risk: nullableInt(get(row, "ghost_score")), remote: flag(get(row, "remote")), fresher: flag(get(row, "fresher_friendly"))}
		if x.title == "" || x.company == "" || x.posted < 0 || x.seen < 0 || x.closed < 0 {
			idx.Info.Rejected++
			continue
		}
		identity := sha256.Sum256([]byte(x.url))
		copy(x.id[:], identity[:12])
		if _, ok := idx.byID[x.id]; ok {
			idx.Info.Duplicates++
			continue
		}
		x.reasons = get(row, "ghost_reasons")
		x.openingsValue(get(row, "openings"))
		x.payMin, _ = strconv.ParseFloat(get(row, "pay_min"), 64)
		x.payMax, _ = strconv.ParseFloat(get(row, "pay_max"), 64)
		x.search = strings.ToLower(x.title + " " + x.company + " " + x.location + " " + x.skills + " " + x.team)
		idx.byID[x.id] = len(idx.rows)
		idx.rows = append(idx.rows, x)
		if x.closed > 0 {
			idx.Info.Closed++
		} else {
			idx.Info.Open++
			idx.Info.Fields[x.field]++
			idx.Info.Levels[x.level]++
			kind := "job"
			if x.employment == "internship" || x.level == "intern" {
				kind = "internship"
			}
			idx.Info.Kinds[kind]++
			if x.remote == 1 {
				idx.Info.Remote++
			}
			if x.fresher == 1 {
				idx.Info.Fresher++
			}
			if x.payMin > 0 || x.payMax > 0 {
				idx.Info.Pay++
			}
		}
	}
	if len(idx.rows) == 0 {
		return nil, errors.New("the dataset contains no usable job records")
	}
	if idx.Info.AsOf == "" {
		for _, x := range idx.rows {
			if d := dateText(max(x.seen, x.closed)); d > idx.Info.AsOf {
				idx.Info.AsOf = d
			}
		}
	}
	if date(idx.Info.AsOf) <= 0 {
		return nil, errors.New("the dataset snapshot date could not be determined")
	}
	if len(idx.Info.Companies) > 20 {
		idx.Info.Companies = idx.Info.Companies[:20]
	}
	latestSkills := []map[string]string{}
	for _, r := range idx.Info.Skills {
		if r["date"] == idx.Info.AsOf {
			latestSkills = append(latestSkills, r)
		}
	}
	idx.Info.Skills = latestSkills
	idx.newest = make([]int, len(idx.rows))
	for i := range idx.rows {
		idx.newest[i] = i
	}
	sort.SliceStable(idx.newest, func(i, j int) bool {
		a, b := idx.rows[idx.newest[i]], idx.rows[idx.newest[j]]
		ad, bd := a.posted, b.posted
		if ad == 0 {
			ad = a.seen
		}
		if bd == 0 {
			bd = b.seen
		}
		return ad > bd
	})
	return idx, nil
}

func (r *record) openingsValue(raw string) {
	n, err := strconv.Atoi(raw)
	if err == nil && n > 0 && n < 1e7 {
		r.openings = int32(n)
	} else {
		r.openings = 1
	}
}

func table(file *zip.File) ([]map[string]string, error) {
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	c := csv.NewReader(io.LimitReader(r, 8<<20))
	head, err := c.Read()
	if err != nil {
		return nil, err
	}
	rows := []map[string]string{}
	for {
		row, err := c.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rows) >= 20000 {
			return nil, errors.New("dataset analytics table is too large")
		}
		values := map[string]string{}
		for i, name := range head {
			values[name] = row[i]
		}
		rows = append(rows, values)
	}
	return rows, nil
}

func (i *Index) Opportunity(x record) model.Opportunity {
	remote, fresher := x.remote == 1, x.fresher == 1
	job := &model.JobDetails{Provider: "RoleBase · Kaggle", ProviderURL: DatasetURL, Field: x.field, Level: x.level, Team: x.team, EmploymentType: strings.ReplaceAll(x.employment, "_", " "), Openings: int(x.openings), DatasetAsOf: i.Info.AsOf, ClosedAt: dateText(x.closed), SourceATS: x.ats, RiskReasons: x.reasons}
	if x.skills != "" {
		job.Skills = strings.Split(x.skills, ";")
	}
	if x.remote >= 0 {
		job.Remote = &remote
	}
	if x.fresher >= 0 {
		job.FresherFriendly = &fresher
	}
	if x.years >= 0 {
		v := int(x.years)
		job.YearsExperience = &v
	}
	if x.risk >= 0 {
		v := int(x.risk)
		job.RiskScore = &v
	}
	if x.payMin > 0 || x.payMax > 0 {
		minimum, maximum := strconv.FormatFloat(x.payMin, 'f', -1, 64), strconv.FormatFloat(x.payMax, 'f', -1, 64)
		job.Salary = fmt.Sprintf("%s %s–%s per year", x.currency, minimum, maximum)
		if x.payMin == 0 {
			job.Salary = fmt.Sprintf("%s up to %s per year", x.currency, maximum)
		}
		if x.payMax == 0 {
			job.Salary = fmt.Sprintf("%s from %s per year", x.currency, minimum)
		}
	}
	asOf, _ := time.Parse("2006-01-02", i.Info.AsOf)
	seen := time.Unix(int64(x.seen)*86400, 0).UTC()
	if x.seen == 0 {
		seen = asOf
	}
	o := model.Opportunity{ID: "kaggle-" + hex.EncodeToString(x.id[:]), URL: x.url, Title: x.title, Organizer: x.company, Kind: "job", Job: job, ApplicationURL: x.url, Location: x.location, Region: "Not stated", DeadlineState: "unknown", SourceID: SourceID, SourceName: "RoleBase · Kaggle job catalogue", SourceKind: "kaggle_dataset", SourceURL: DatasetURL, CaptureMethod: "kaggle_snapshot", ExtractionVersion: "rolebase-facts-1", FirstSeenAt: seen, LastFetchedAt: asOf, UpdatedAt: asOf, Tags: job.Skills, Eligibility: []string{}, Benefits: []string{}, Missing: []string{"full job description", "confirmed application deadline"}, Evidence: map[string]string{"dataset": DatasetURL, "snapshot_date": i.Info.AsOf, "classifications": "Field, level, skills, experience and risk signals are dataset-derived; verify with the employer."}}
	if x.posted > 0 {
		d := time.Unix(int64(x.posted)*86400, 0).UTC()
		o.PublishedAt = &d
	}
	if x.level == "intern" || x.employment == "internship" {
		o.Kind = "internship"
	}
	o.Summary = fmt.Sprintf("%s · %s · %s. Structured employer-listing facts captured in the %s RoleBase dataset.", x.company, x.level, x.field, i.Info.AsOf)
	o.Body = fmt.Sprintf("Structured dataset facts (original descriptions are not included).\nEmployer: %s\nTitle: %s\nLocation: %s\nField: %s\nLevel: %s\nSkills: %s\nSnapshot: %s\nApply: %s", x.company, x.title, x.location, x.field, x.level, x.skills, i.Info.AsOf, x.url)
	if x.years >= 0 {
		o.Eligibility = append(o.Eligibility, fmt.Sprintf("Dataset extracted minimum experience: %d years. Confirm the employer's requirements.", x.years))
	}
	o.Decorate(time.Now())
	return o
}

func Match(o model.Opportunity, q model.Query) bool {
	if q.JobField != "" && (o.Job == nil || o.Job.Field != q.JobField) {
		return false
	}
	if q.JobLevel != "" && (o.Job == nil || o.Job.Level != q.JobLevel) {
		return false
	}
	if q.RemoteOnly && (o.Job == nil || o.Job.Remote == nil || !*o.Job.Remote) {
		return false
	}
	if q.FresherOnly && (o.Job == nil || o.Job.FresherFriendly == nil || !*o.Job.FresherFriendly) {
		return false
	}
	if q.Skill != "" && (o.Job == nil || !model.Contains(o.Job.Skills, strings.ToLower(q.Skill))) {
		return false
	}
	if q.MaxYears != nil && (o.Job == nil || o.Job.YearsExperience == nil || *o.Job.YearsExperience > *q.MaxYears) {
		return false
	}
	if q.MaxRisk != nil && (o.Job == nil || o.Job.RiskScore == nil || *o.Job.RiskScore > *q.MaxRisk) {
		return false
	}
	return true
}

func (i *Index) List(ctx context.Context, q model.Query, exclude map[string]bool) ([]model.Opportunity, int, error) {
	items := []model.Opportunity{}
	limit := q.Limit
	if limit <= 0 {
		limit = 48
	}
	limit = min(limit, 10000)
	total, err := i.walk(ctx, q, exclude, func(x record, n int) {
		if n >= q.Offset && len(items) < limit {
			items = append(items, i.Opportunity(x))
		}
	})
	return items, total, err
}

func (i *Index) walk(ctx context.Context, q model.Query, exclude map[string]bool, visit func(record, int)) (int, error) {
	total := 0
	if q.Platform != "" && q.Platform != "kaggle" || q.Source != "" && q.Source != SourceID || q.Region != "" && q.Region != "Not stated" || q.Status == "closing" || q.Status == "open" || q.Status == "rolling" {
		return 0, nil
	}
	search := strings.ToLower(q.Search)
	for n, pos := range i.newest {
		if n%10000 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		x := i.rows[pos]
		closed := x.closed > 0
		if q.Status == "closed" && !closed || q.Status != "closed" && q.Status != "all" && closed {
			continue
		}
		kind := "job"
		if x.level == "intern" || x.employment == "internship" {
			kind = "internship"
		}
		if q.Kind != "" && !model.Contains(strings.Split(q.Kind, ","), kind) {
			continue
		}
		if search != "" && !strings.Contains(x.search, search) {
			continue
		}
		if q.JobField != "" && x.field != q.JobField || q.JobLevel != "" && x.level != q.JobLevel || q.RemoteOnly && x.remote != 1 || q.FresherOnly && x.fresher != 1 || q.MaxYears != nil && (x.years < 0 || int(x.years) > *q.MaxYears) || q.MaxRisk != nil && (x.risk < 0 || int(x.risk) > *q.MaxRisk) {
			continue
		}
		if q.Skill != "" && !model.Contains(strings.Split(x.skills, ";"), strings.ToLower(q.Skill)) {
			continue
		}
		if exclude[x.url] {
			continue
		}
		if len(q.IDs) > 0 && !model.Contains(q.IDs, "kaggle-"+hex.EncodeToString(x.id[:])) {
			continue
		}
		visit(x, total)
		total++
	}
	return total, nil
}

func (i *Index) Get(id string) (model.Opportunity, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(id, "kaggle-"))
	if err != nil || len(raw) != 12 {
		return model.Opportunity{}, errors.New("record not found")
	}
	var key [12]byte
	copy(key[:], raw)
	if pos, ok := i.byID[key]; ok {
		return i.Opportunity(i.rows[pos]), nil
	}
	return model.Opportunity{}, errors.New("record not found")
}
