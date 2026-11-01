// Package store keeps donor and team history in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // SQL driver
)

const schema = `
CREATE TABLE IF NOT EXISTS donors (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	baseline_score INTEGER NOT NULL,
	baseline_wus INTEGER NOT NULL,
	baseline_error INTEGER NOT NULL,
	first_seen INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS samples (
	donor_id INTEGER NOT NULL REFERENCES donors(id),
	ts INTEGER NOT NULL,
	score INTEGER NOT NULL,
	wus INTEGER NOT NULL,
	PRIMARY KEY (donor_id, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS team_samples (
	team INTEGER NOT NULL,
	ts INTEGER NOT NULL,
	month INTEGER NOT NULL,
	name TEXT NOT NULL,
	credit INTEGER NOT NULL,
	wus INTEGER NOT NULL,
	PRIMARY KEY (team, ts, month)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS missing_names (
	name TEXT PRIMARY KEY
) WITHOUT ROWID;
`

// Donor is a tracked participant. Points earned in the event are the current
// team score minus BaselineScore. BaselineError is the largest number of
// points the baseline can be off by. It is zero when the baseline came from
// the donor's monthly credit.
type Donor struct {
	ID            int64
	Name          string
	BaselineScore int64
	BaselineWUs   int64
	BaselineError int64
	FirstSeen     time.Time
}

// Sample is a donor's team score at a point in time.
type Sample struct {
	DonorID int64
	At      time.Time
	Score   int64
	WUs     int64
}

// TeamSample is a team's credit for one month at a point in time. Month is
// written as year*100+month.
type TeamSample struct {
	Team   int
	At     time.Time
	Month  int
	Name   string
	Credit int64
	WUs    int64
}

// Store is the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens the database at path and applies the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("apply schema: %w", err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// UpsertDonor adds a donor or replaces its name and baseline.
func (s *Store) UpsertDonor(ctx context.Context, d Donor) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO donors (id, name, baseline_score, baseline_wus, baseline_error, first_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name,
			baseline_score = excluded.baseline_score,
			baseline_wus = excluded.baseline_wus,
			baseline_error = excluded.baseline_error`,
		d.ID, d.Name, d.BaselineScore, d.BaselineWUs, d.BaselineError, d.FirstSeen.Unix())
	if err != nil {
		return fmt.Errorf("upsert donor %d: %w", d.ID, err)
	}
	return nil
}

// Donors returns all tracked donors.
func (s *Store) Donors(ctx context.Context) (out []Donor, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, baseline_score, baseline_wus, baseline_error, first_seen FROM donors`)
	if err != nil {
		return nil, fmt.Errorf("query donors: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var d Donor
		var firstSeen int64
		if err := rows.Scan(&d.ID, &d.Name, &d.BaselineScore, &d.BaselineWUs, &d.BaselineError, &firstSeen); err != nil {
			return nil, fmt.Errorf("scan donor: %w", err)
		}
		d.FirstSeen = time.Unix(firstSeen, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// AddMissing records names the API did not know.
func (s *Store) AddMissing(ctx context.Context, names []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, name := range names {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO missing_names (name) VALUES (?)`, name); err != nil {
				return fmt.Errorf("insert missing name: %w", err)
			}
		}
		return nil
	})
}

// Missing returns every name the API has not known during the event.
func (s *Store) Missing(ctx context.Context) (out []string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM missing_names`)
	if err != nil {
		return nil, fmt.Errorf("query missing names: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan missing name: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// AddSamples writes samples in one transaction.
func (s *Store) AddSamples(ctx context.Context, samples []Sample) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, v := range samples {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO samples (donor_id, ts, score, wus) VALUES (?, ?, ?, ?)`,
				v.DonorID, v.At.Unix(), v.Score, v.WUs); err != nil {
				return fmt.Errorf("insert sample: %w", err)
			}
		}
		return nil
	})
}

// Samples returns all donor samples, oldest first.
func (s *Store) Samples(ctx context.Context) (out []Sample, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT donor_id, ts, score, wus FROM samples ORDER BY ts`)
	if err != nil {
		return nil, fmt.Errorf("query samples: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var v Sample
		var ts int64
		if err := rows.Scan(&v.DonorID, &ts, &v.Score, &v.WUs); err != nil {
			return nil, fmt.Errorf("scan sample: %w", err)
		}
		v.At = time.Unix(ts, 0).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// AddTeamSamples writes team samples in one transaction.
func (s *Store) AddTeamSamples(ctx context.Context, samples []TeamSample) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, v := range samples {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO team_samples (team, ts, month, name, credit, wus) VALUES (?, ?, ?, ?, ?, ?)`,
				v.Team, v.At.Unix(), v.Month, v.Name, v.Credit, v.WUs); err != nil {
				return fmt.Errorf("insert team sample: %w", err)
			}
		}
		return nil
	})
}

// TeamSamples returns all team samples, oldest first.
func (s *Store) TeamSamples(ctx context.Context) (out []TeamSample, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT team, ts, month, name, credit, wus FROM team_samples ORDER BY ts`)
	if err != nil {
		return nil, fmt.Errorf("query team samples: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var v TeamSample
		var ts int64
		if err := rows.Scan(&v.Team, &ts, &v.Month, &v.Name, &v.Credit, &v.WUs); err != nil {
			return nil, fmt.Errorf("scan team sample: %w", err)
		}
		v.At = time.Unix(ts, 0).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
