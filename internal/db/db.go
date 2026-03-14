package db

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"photo-dedup/internal/hasher"
)

// HashRecord represents one indexed image in the database.
type HashRecord struct {
	ID       int64
	DHash0   string // hex hash at 0° rotation
	DHash90  string // hex hash at 90°
	DHash180 string // hex hash at 180°
	DHash270 string // hex hash at 270°
	PathHint string // last known file path (display only)
}

// Candidate represents a potential match found during dedup checking.
type Candidate struct {
	ID              int64
	PathHint        string
	MatchedRotation int    // which rotation of the DB entry matched
	MatchedHash     string // the hash value that matched
	Distance        int    // Hamming distance (0 = exact match)
}

// Stats holds index statistics.
type Stats struct {
	TotalImages int
	DBSizeBytes int64
}

// Database wraps SQLite operations for the hash index.
type Database struct {
	db   *sql.DB
	mu   sync.Mutex
	path string
}

// Open opens or creates the SQLite database at the given path.
func Open(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Configure for performance and safety.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=30000",
		"PRAGMA cache_size=-64000", // 64MB cache
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("set pragma %q: %w", p, err)
		}
	}

	d := &Database{db: db, path: dbPath}
	if err := d.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database connection.
func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS hashes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		dhash_0    TEXT NOT NULL,
		dhash_90   TEXT NOT NULL,
		dhash_180  TEXT NOT NULL,
		dhash_270  TEXT NOT NULL,
		path_hint  TEXT,
		indexed_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_dhash_0   ON hashes(dhash_0);
	CREATE INDEX IF NOT EXISTS idx_dhash_90  ON hashes(dhash_90);
	CREATE INDEX IF NOT EXISTS idx_dhash_180 ON hashes(dhash_180);
	CREATE INDEX IF NOT EXISTS idx_dhash_270 ON hashes(dhash_270);
	`
	_, err := d.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

// InsertHash adds a new hash record to the database.
func (d *Database) InsertHash(dhash0, dhash90, dhash180, dhash270, pathHint string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(
		`INSERT INTO hashes (dhash_0, dhash_90, dhash_180, dhash_270, path_hint, indexed_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		dhash0, dhash90, dhash180, dhash270, pathHint, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// InsertBatch inserts multiple hash records in a single transaction.
func (d *Database) InsertBatch(records []HashRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO hashes (dhash_0, dhash_90, dhash_180, dhash_270, path_hint, indexed_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range records {
		if _, err := stmt.Exec(r.DHash0, r.DHash90, r.DHash180, r.DHash270, r.PathHint, now); err != nil {
			return fmt.Errorf("insert record: %w", err)
		}
	}

	return tx.Commit()
}

// ExactMatch checks if any of the 4 provided hashes exist in the database
// as an exact match on any rotation column. Returns true if found.
func (d *Database) ExactMatch(dhash0, dhash90, dhash180, dhash270 string) (bool, error) {
	query := `
	SELECT COUNT(*) FROM hashes
	WHERE dhash_0   IN (?, ?, ?, ?)
	   OR dhash_90  IN (?, ?, ?, ?)
	   OR dhash_180 IN (?, ?, ?, ?)
	   OR dhash_270 IN (?, ?, ?, ?)
	LIMIT 1`

	hashes := []interface{}{
		dhash0, dhash90, dhash180, dhash270,
		dhash0, dhash90, dhash180, dhash270,
		dhash0, dhash90, dhash180, dhash270,
		dhash0, dhash90, dhash180, dhash270,
	}

	var count int
	err := d.db.QueryRow(query, hashes...).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("exact match query: %w", err)
	}
	return count > 0, nil
}

// FindCandidates searches for all records within the given Hamming distance
// of any of the 4 query hashes. Returns candidates sorted by distance.
func (d *Database) FindCandidates(dhash0, dhash90, dhash180, dhash270 string, threshold int) ([]Candidate, error) {
	queryHashes := map[int]string{
		0:   dhash0,
		90:  dhash90,
		180: dhash180,
		270: dhash270,
	}

	// Load all hashes from DB into memory for Hamming distance comparison.
	// With 200K records this is ~50MB — acceptable.
	rows, err := d.db.Query(`SELECT id, dhash_0, dhash_90, dhash_180, dhash_270, path_hint FROM hashes`)
	if err != nil {
		return nil, fmt.Errorf("load hashes: %w", err)
	}
	defer rows.Close()

	var candidates []Candidate

	for rows.Next() {
		var rec HashRecord
		if err := rows.Scan(&rec.ID, &rec.DHash0, &rec.DHash90, &rec.DHash180, &rec.DHash270, &rec.PathHint); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		dbHashes := map[int]string{
			0:   rec.DHash0,
			90:  rec.DHash90,
			180: rec.DHash180,
			270: rec.DHash270,
		}

		// Compare every query rotation against every DB rotation.
		bestDist := threshold + 1
		bestRotation := 0
		bestHash := ""

		for _, qHash := range queryHashes {
			for rot, dHash := range dbHashes {
				dist := hasher.HammingDistanceHex(qHash, dHash)
				if dist < bestDist {
					bestDist = dist
					bestRotation = rot
					bestHash = dHash
				}
			}
		}

		if bestDist <= threshold {
			candidates = append(candidates, Candidate{
				ID:              rec.ID,
				PathHint:        rec.PathHint,
				MatchedRotation: bestRotation,
				MatchedHash:     bestHash,
				Distance:        bestDist,
			})
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return candidates, nil
}

// GetStats returns index statistics.
func (d *Database) GetStats() (Stats, error) {
	var s Stats
	err := d.db.QueryRow(`SELECT COUNT(*) FROM hashes`).Scan(&s.TotalImages)
	if err != nil {
		return s, fmt.Errorf("count hashes: %w", err)
	}

	// Get DB file size.
	// SQLite page_count * page_size gives the logical size.
	var pageCount, pageSize int64
	d.db.QueryRow(`PRAGMA page_count`).Scan(&pageCount)
	d.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	s.DBSizeBytes = pageCount * pageSize

	return s, nil
}

