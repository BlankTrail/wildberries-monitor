// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file is the only place a Job and a stored row know about each other.
//
// The direction is fixed and it is why this lives here rather than in the
// store: the engine knows about storage, storage does not know about the
// engine, so the mapping belongs on the side that is allowed to see both.
// store.JobRow deliberately holds strings and opaque JSON for the same
// reason.

// params is the shape of JobRow.Params.
//
// Kept as a named type with explicit tags rather than a map, because a typo
// in a key spelled inline would round-trip cleanly and lose a parameter in
// silence — a saved job would come back missing the brand it was built for.
//
// Phrases and PhraseListID are alternatives, not a pair. A handful of phrases
// typed into the form travel in the params; an uploaded file does not, and
// the id points at the rows instead. Putting a hundred thousand phrases in
// this column would make every read of the job — every listing, every
// schedule check — pay several megabytes to learn the job's name.
type params struct {
	Phrases         []string `json:"phrases,omitempty"`
	PhraseListID    int64    `json:"phrase_list_id,omitempty"`
	PhraseListCount int      `json:"phrase_list_count,omitempty"`
	SupplierID      int64    `json:"supplier_id,omitempty"`
	BrandID         int64    `json:"brand_id,omitempty"`
	Articles        []int64  `json:"articles,omitempty"`
	AppType         int      `json:"app_type"`
	// Input is what somebody pasted for a profile job, kept as they typed it.
	Input string `json:"input,omitempty"`

	MaxPages int `json:"max_pages,omitempty"`
}

// Save writes a job and returns its id.
//
// Validate is called first, and its failure is returned rather than stored:
// a job that cannot run is not worth a row, and the constructor shows the
// same message either way.
func Save(ctx context.Context, s *store.Store, j Job) (int64, error) {
	if err := j.Validate(); err != nil {
		return 0, err
	}

	p, err := json.Marshal(params{
		Phrases:         j.Phrases,
		PhraseListID:    j.PhraseListID,
		PhraseListCount: j.PhraseListCount,
		SupplierID:      j.SupplierID,
		BrandID:         j.BrandID,
		Articles:        j.Articles,
		AppType:         j.AppType,
		Input:           j.Input,
		MaxPages:        j.MaxPages,
	})
	if err != nil {
		return 0, fmt.Errorf("job: save: %w", err)
	}
	fields, err := json.Marshal([]string(j.Fields))
	if err != nil {
		return 0, fmt.Errorf("job: save: %w", err)
	}
	regions, err := json.Marshal(j.Regions)
	if err != nil {
		return 0, fmt.Errorf("job: save: %w", err)
	}

	return s.SaveJob(ctx, store.JobRow{
		ID:       j.ID,
		Name:     j.Name,
		Type:     string(j.Kind),
		Params:   string(p),
		Fields:   string(fields),
		Regions:  string(regions),
		Channels: "[]",
		Schedule: j.Schedule,
		Threads:  j.Threads,
		DelayMS:  int(j.Delay / time.Millisecond),
		Enabled:  j.Enabled,
	})
}

// Load reads a job back.
//
// A field key this build does not declare is kept rather than dropped:
// Validate reports it by name, which is how a job saved by a newer release is
// noticed instead of quietly running with one column missing.
func Load(ctx context.Context, s *store.Store, id int64) (Job, error) {
	row, err := s.Job(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return fromRow(row)
}

func fromRow(row store.JobRow) (Job, error) {
	var p params
	if err := json.Unmarshal([]byte(row.Params), &p); err != nil {
		return Job{}, fmt.Errorf("job %d: params: %w", row.ID, err)
	}
	var fields []string
	if err := json.Unmarshal([]byte(row.Fields), &fields); err != nil {
		return Job{}, fmt.Errorf("job %d: fields: %w", row.ID, err)
	}
	var regions []string
	if err := json.Unmarshal([]byte(row.Regions), &regions); err != nil {
		return Job{}, fmt.Errorf("job %d: regions: %w", row.ID, err)
	}

	return Job{
		ID:              row.ID,
		Name:            row.Name,
		Kind:            Kind(row.Type),
		Phrases:         p.Phrases,
		PhraseListID:    p.PhraseListID,
		PhraseListCount: p.PhraseListCount,
		SupplierID:      p.SupplierID,
		BrandID:         p.BrandID,
		Articles:        p.Articles,
		Regions:         regions,
		AppType:         p.AppType,
		Fields:          wb.Selection(fields),
		Input:           p.Input,
		MaxPages:        p.MaxPages,
		Threads:         row.Threads,
		Delay:           time.Duration(row.DelayMS) * time.Millisecond,
		Schedule:        row.Schedule,
		Enabled:         row.Enabled,
	}, nil
}
