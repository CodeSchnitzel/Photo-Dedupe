package db

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/bits"
	"sync"
	"time"

	_ "modernc.org/sqlite"
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
// NOTE: For batch checking, prefer HashIndex.FindMatch which avoids repeated DB scans.
func (d *Database) FindCandidates(dhash0, dhash90, dhash180, dhash270 string, threshold int) ([]Candidate, error) {
	rows, err := d.db.Query(`SELECT id, dhash_0, dhash_90, dhash_180, dhash_270, path_hint FROM hashes`)
	if err != nil {
		return nil, fmt.Errorf("load hashes: %w", err)
	}
	defer rows.Close()

	queryBytes := [4][]byte{hexDecode(dhash0), hexDecode(dhash90), hexDecode(dhash180), hexDecode(dhash270)}
	var candidates []Candidate

	for rows.Next() {
		var rec HashRecord
		if err := rows.Scan(&rec.ID, &rec.DHash0, &rec.DHash90, &rec.DHash180, &rec.DHash270, &rec.PathHint); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		dbBytes := [4][]byte{hexDecode(rec.DHash0), hexDecode(rec.DHash90), hexDecode(rec.DHash180), hexDecode(rec.DHash270)}
		bestDist, bestRot, bestHash := bestMatch(queryBytes, dbBytes, [4]string{rec.DHash0, rec.DHash90, rec.DHash180, rec.DHash270}, threshold)

		if bestDist <= threshold {
			candidates = append(candidates, Candidate{
				ID:              rec.ID,
				PathHint:        rec.PathHint,
				MatchedRotation: bestRot,
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

// --- In-Memory Hash Index ---

// indexEntry holds pre-decoded binary hashes for fast Hamming comparison.
type indexEntry struct {
	id         int64
	pathHint   string
	hashes     [4][]byte // 0°, 90°, 180°, 270° as raw bytes
	hexes      [4]string // same as hex strings for result reporting
	lowEntropy bool      // true if any rotation hash has degenerate bit density
}

// EntropyMinBits and EntropyMaxBits define the set-bit thresholds for
// degenerate hashes. A 256-bit hash with fewer than Min or more than Max
// set bits is considered low-entropy (e.g., nearly all-black or all-white).
// Near-match comparisons are skipped for such hashes to avoid false positives.
const (
	EntropyMinBits = 20  // fewer than this → nearly all zeros
	EntropyMaxBits = 236 // more than this → nearly all ones
)

// bitCount returns the number of set bits in a byte slice.
func bitCount(b []byte) int {
	n := 0
	for _, v := range b {
		n += bits.OnesCount8(v)
	}
	return n
}

// IsLowEntropyHex checks whether 4 hex-encoded hashes have degenerate bit density.
// Exported for use by the checker to log skipped files.
func IsLowEntropyHex(dhash0, dhash90, dhash180, dhash270 string) bool {
	return isLowEntropy([4][]byte{hexDecode(dhash0), hexDecode(dhash90), hexDecode(dhash180), hexDecode(dhash270)})
}

// isLowEntropy returns true if any of the 4 rotation hashes has degenerate
// bit density (too few or too many set bits).
func isLowEntropy(hashes [4][]byte) bool {
	for _, h := range hashes {
		bc := bitCount(h)
		if bc < EntropyMinBits || bc > EntropyMaxBits {
			return true
		}
	}
	return false
}

// HashIndex is a read-only in-memory index of all hashes for fast batch lookups.
// It loads the entire DB once and supports concurrent read access.
type HashIndex struct {
	entries  []indexEntry
	exactMap map[string]int // hex hash → index into entries (first match)
}

// LoadHashIndex loads all hash records from the database into memory.
// For 180K records with 256-bit hashes, this uses ~60MB.
func (d *Database) LoadHashIndex() (*HashIndex, error) {
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM hashes`).Scan(&count); err != nil {
		return nil, fmt.Errorf("count hashes: %w", err)
	}

	idx := &HashIndex{
		entries:  make([]indexEntry, 0, count),
		exactMap: make(map[string]int, count*4),
	}

	rows, err := d.db.Query(`SELECT id, dhash_0, dhash_90, dhash_180, dhash_270, path_hint FROM hashes`)
	if err != nil {
		return nil, fmt.Errorf("load hashes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rec HashRecord
		if err := rows.Scan(&rec.ID, &rec.DHash0, &rec.DHash90, &rec.DHash180, &rec.DHash270, &rec.PathHint); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		rawHashes := [4][]byte{hexDecode(rec.DHash0), hexDecode(rec.DHash90), hexDecode(rec.DHash180), hexDecode(rec.DHash270)}
		e := indexEntry{
			id:         rec.ID,
			pathHint:   rec.PathHint,
			hexes:      [4]string{rec.DHash0, rec.DHash90, rec.DHash180, rec.DHash270},
			hashes:     rawHashes,
			lowEntropy: isLowEntropy(rawHashes),
		}

		i := len(idx.entries)
		idx.entries = append(idx.entries, e)

		// Index each rotation hash for O(1) exact lookup.
		for _, h := range e.hexes {
			if _, exists := idx.exactMap[h]; !exists {
				idx.exactMap[h] = i
			}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return idx, nil
}

// Count returns the number of entries in the index.
func (idx *HashIndex) Count() int {
	return len(idx.entries)
}

// ExactMatch checks for an exact hash match using the O(1) map lookup.
func (idx *HashIndex) ExactMatch(dhash0, dhash90, dhash180, dhash270 string) (bool, string) {
	for _, h := range [4]string{dhash0, dhash90, dhash180, dhash270} {
		if i, ok := idx.exactMap[h]; ok {
			return true, idx.entries[i].pathHint
		}
	}
	return false, ""
}

// FindMatch searches for the best match within the given Hamming distance threshold.
// Returns the best candidate (if any). Thread-safe for concurrent reads.
// Skips entries where either the query or DB hash has degenerate entropy to avoid
// false positives on low-information images (e.g., nearly all-black or all-white).
func (idx *HashIndex) FindMatch(dhash0, dhash90, dhash180, dhash270 string, threshold int) (Candidate, bool) {
	queryBytes := [4][]byte{hexDecode(dhash0), hexDecode(dhash90), hexDecode(dhash180), hexDecode(dhash270)}
	queryLowEntropy := isLowEntropy(queryBytes)

	bestDist := threshold + 1
	bestIdx := -1
	bestRot := 0
	bestHash := ""

	for i := range idx.entries {
		e := &idx.entries[i]
		// Skip near-match comparison for degenerate hashes on either side.
		if queryLowEntropy || e.lowEntropy {
			continue
		}
		dist, rot, hash := bestMatchEntry(queryBytes, e, threshold)
		if dist < bestDist {
			bestDist = dist
			bestIdx = i
			bestRot = rot
			bestHash = hash
			if dist == 0 {
				break // Can't do better than exact.
			}
		}
	}

	if bestIdx < 0 {
		return Candidate{}, false
	}

	e := &idx.entries[bestIdx]
	return Candidate{
		ID:              e.id,
		PathHint:        e.pathHint,
		MatchedRotation: bestRot,
		MatchedHash:     bestHash,
		Distance:        bestDist,
	}, true
}

// bestMatchEntry compares 4 query hashes against one index entry's 4 hashes (16 pairs).
func bestMatchEntry(queryBytes [4][]byte, e *indexEntry, threshold int) (dist int, rotation int, hashHex string) {
	bestDist := threshold + 1
	rotations := [4]int{0, 90, 180, 270}

	for _, qb := range queryBytes {
		for ri, db := range e.hashes {
			d := hammingBytes(qb, db)
			if d < bestDist {
				bestDist = d
				rotation = rotations[ri]
				hashHex = e.hexes[ri]
				if d == 0 {
					return 0, rotation, hashHex
				}
			}
		}
	}
	return bestDist, rotation, hashHex
}

// --- Hamming distance helpers (operate on raw bytes, no hex decode per call) ---

func hammingBytes(a, b []byte) int {
	if len(a) != len(b) {
		return 999
	}
	d := 0
	for i := range a {
		d += bits.OnesCount8(a[i] ^ b[i])
	}
	return d
}

func hexDecode(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

func bestMatch(queryBytes [4][]byte, dbBytes [4][]byte, dbHexes [4]string, threshold int) (int, int, string) {
	bestDist := threshold + 1
	bestRot := 0
	bestHash := ""
	rotations := [4]int{0, 90, 180, 270}

	for _, qb := range queryBytes {
		for ri, db := range dbBytes {
			d := hammingBytes(qb, db)
			if d < bestDist {
				bestDist = d
				bestRot = rotations[ri]
				bestHash = dbHexes[ri]
				if d == 0 {
					return 0, bestRot, bestHash
				}
			}
		}
	}
	return bestDist, bestRot, bestHash
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

