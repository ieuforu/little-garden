package garden

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE gardens (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 active INTEGER NOT NULL DEFAULT 1,
 paused INTEGER NOT NULL DEFAULT 0,
 revision INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX one_active_garden ON gardens(active) WHERE active = 1;
CREATE TABLE flowers (
 id TEXT PRIMARY KEY,
 garden_id INTEGER NOT NULL REFERENCES gardens(id),
 visitor_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 x REAL NOT NULL, y REAL NOT NULL,
 seed INTEGER NOT NULL,
 palette INTEGER NOT NULL CHECK(palette BETWEEN 0 AND 3),
 style_version INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL,
 hidden INTEGER NOT NULL DEFAULT 0,
 UNIQUE(visitor_id, request_id)
);
CREATE INDEX flowers_garden ON flowers(garden_id, hidden);
CREATE INDEX flowers_visitor ON flowers(visitor_id);
CREATE TABLE bans (visitor_id TEXT PRIMARY KEY);
PRAGMA user_version = 1;
`

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize(ctx context.Context) error {
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON", "PRAGMA max_page_count=16384", "PRAGMA journal_size_limit=8388608",
	} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database schema %d is newer than supported schema 1", version)
	}
	if version == 1 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO gardens(created_at) VALUES (?)", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	// A real, persisted starter planting; these are not simulated online visitors.
	points := [][2]float64{{240, 460}, {440, 780}, {660, 360}, {830, 950}, {1050, 620}, {1280, 310}, {1510, 860}, {1740, 480}, {2050, 720}, {2210, 330}, {350, 1210}, {700, 1280}, {1170, 1140}, {1460, 1270}, {1880, 1190}, {2180, 1060}}
	for i, p := range points {
		_, err = tx.ExecContext(ctx, `INSERT INTO flowers(id,garden_id,visitor_id,request_id,x,y,seed,palette,created_at) VALUES (?,1,'gardener',?,?,?,?,?,?)`,
			fmt.Sprintf("starter-%02d", i), fmt.Sprintf("starter-%02d", i), p[0], p[1], 701+i*1937, i%4, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const flowerColumns = "id,garden_id,x,y,seed,palette,style_version,created_at"

type scanner interface{ Scan(...any) error }

func scanFlower(row scanner) (f Flower, err error) {
	err = row.Scan(&f.ID, &f.GardenID, &f.X, &f.Y, &f.Seed, &f.Palette, &f.StyleVersion, &f.CreatedAt)
	return
}

func (s *Store) load(ctx context.Context) (Snapshot, map[string]bool, error) {
	state := Snapshot{Type: "snapshot", Flowers: make([]Flower, 0)}
	state.Garden.Width, state.Garden.Height, state.Garden.Capacity = WorldWidth, WorldHeight, Capacity
	err := s.db.QueryRowContext(ctx, "SELECT id,revision,paused FROM gardens WHERE active=1").Scan(&state.Garden.ID, &state.Garden.Revision, &state.Garden.Paused)
	if err != nil {
		return state, nil, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM flowers WHERE garden_id=?", state.Garden.ID).Scan(&state.Garden.Planted)
	if err != nil {
		return state, nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+flowerColumns+" FROM flowers WHERE garden_id=? AND hidden=0 ORDER BY created_at,id", state.Garden.ID)
	if err != nil {
		return state, nil, err
	}
	for rows.Next() {
		f, e := scanFlower(rows)
		if e != nil {
			rows.Close()
			return state, nil, e
		}
		state.Flowers = append(state.Flowers, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, nil, err
	}
	bans := make(map[string]bool)
	rows, err = s.db.QueryContext(ctx, "SELECT visitor_id FROM bans")
	if err != nil {
		return state, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return state, nil, err
		}
		bans[id] = true
	}
	return state, bans, rows.Err()
}

func (s *Store) existing(ctx context.Context, visitor, request string) (Flower, error) {
	return scanFlower(s.db.QueryRowContext(ctx, "SELECT "+flowerColumns+" FROM flowers WHERE visitor_id=? AND request_id=?", visitor, request))
}

func (s *Store) plant(ctx context.Context, f Flower, visitor, request string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO flowers(id,garden_id,visitor_id,request_id,x,y,seed,palette,style_version,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, f.ID, f.GardenID, visitor, request, f.X, f.Y, f.Seed, f.Palette, f.StyleVersion, f.CreatedAt)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE gardens SET revision=revision+1 WHERE id=?", f.GardenID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func SchemaVersion(ctx context.Context, path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var version int
	err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}

// Backup uses SQLite's online snapshot mechanism. Never copy a live .db file alone.
func Backup(ctx context.Context, source, destination string) error {
	if _, err := os.Stat(source); err != nil {
		return err
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("backup destination must not exist")
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=10000"); err != nil {
		return err
	}
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return err
	}
	if err = os.Chmod(destination, 0600); err != nil {
		return err
	}
	copy, err := sql.Open("sqlite", destination)
	if err != nil {
		return err
	}
	defer copy.Close()
	var check string
	if err = copy.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("backup integrity check failed: %s", check)
	}
	return nil
}
