package garden

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sync"
	"time"
)

var requestPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,64}$`)

type Subscription struct{ Events chan []byte }
type Engine struct {
	mu     sync.Mutex
	store  *Store
	state  Snapshot
	bans   map[string]bool
	subs   map[*Subscription]struct{}
	writes chan struct{}
}

func New(ctx context.Context, store *Store) (*Engine, error) {
	state, bans, err := store.load(ctx)
	if err != nil {
		return nil, err
	}
	return &Engine{store: store, state: state, bans: bans, subs: make(map[*Subscription]struct{}), writes: make(chan struct{}, 16)}, nil
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state
	s.Flowers = append([]Flower{}, e.state.Flowers...)
	return s
}
func (e *Engine) Banned(visitor string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bans[visitor]
}

func (e *Engine) Subscribe() *Subscription {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := &Subscription{Events: make(chan []byte, 32)}
	// Subscription and snapshot are atomic relative to a committed planting.
	data, _ := json.Marshal(e.state)
	s.Events <- data
	e.subs[s] = struct{}{}
	return s
}
func (e *Engine) Unsubscribe(s *Subscription) { e.mu.Lock(); defer e.mu.Unlock(); e.drop(s) }
func (e *Engine) drop(s *Subscription) {
	if _, ok := e.subs[s]; ok {
		delete(e.subs, s)
		close(s.Events)
	}
}
func (e *Engine) broadcast(value any) {
	data, _ := json.Marshal(value)
	for s := range e.subs {
		select {
		case s.Events <- data:
		default:
			e.drop(s)
		}
	}
}
func (e *Engine) Shutdown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for s := range e.subs {
		e.drop(s)
	}
}

func (e *Engine) Plant(ctx context.Context, visitor string, req PlantRequest) (PlantResult, error) {
	select {
	case e.writes <- struct{}{}:
		defer func() { <-e.writes }()
	default:
		return PlantResult{}, ErrBusy
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PlantResult{}, err
	}
	if !requestPattern.MatchString(req.RequestID) || math.IsNaN(req.X) || math.IsNaN(req.Y) || math.IsInf(req.X, 0) || math.IsInf(req.Y, 0) || req.Palette < 0 || req.Palette > 3 || req.X < 60 || req.X > WorldWidth-60 || req.Y < 100 || req.Y > WorldHeight-200 {
		return PlantResult{}, ErrInvalid
	}
	if e.bans[visitor] {
		return PlantResult{}, ErrBanned
	}
	old, err := e.store.existing(ctx, visitor, req.RequestID)
	if err == nil {
		if old.GardenID != req.GardenID || old.X != req.X || old.Y != req.Y || old.Palette != req.Palette {
			return PlantResult{}, ErrConflict
		}
		return PlantResult{Type: "flower", Revision: e.state.Garden.Revision, Flower: old, Duplicate: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PlantResult{}, err
	}
	if req.GardenID != e.state.Garden.ID {
		return PlantResult{}, ErrStale
	}
	if e.state.Garden.Paused {
		return PlantResult{}, ErrPaused
	}
	if e.state.Garden.Planted >= Capacity {
		return PlantResult{}, ErrFull
	}
	for _, f := range e.state.Flowers {
		if math.Hypot(f.X-req.X, f.Y-req.Y) < Spacing {
			return PlantResult{}, ErrSpacing
		}
	}
	var random [20]byte
	if _, err = rand.Read(random[:]); err != nil {
		return PlantResult{}, err
	}
	f := Flower{ID: hex.EncodeToString(random[:16]), GardenID: req.GardenID, X: req.X, Y: req.Y, Seed: binary.LittleEndian.Uint32(random[16:]), Palette: req.Palette, StyleVersion: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = e.store.plant(ctx, f, visitor, req.RequestID); err != nil {
		return PlantResult{}, err
	}
	e.state.Flowers = append(e.state.Flowers, f)
	e.state.Garden.Revision++
	e.state.Garden.Planted++
	result := PlantResult{Type: "flower", Revision: e.state.Garden.Revision, Flower: f}
	e.broadcast(result)
	return result, nil
}

func (e *Engine) Pause(ctx context.Context, paused bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.store.db.ExecContext(ctx, "UPDATE gardens SET paused=?,revision=revision+1 WHERE id=?", paused, e.state.Garden.ID)
	if err != nil {
		return err
	}
	e.state.Garden.Paused = paused
	e.state.Garden.Revision++
	e.broadcast(e.state)
	return nil
}

func (e *Engine) BanFlower(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var visitor string
	if err = tx.QueryRowContext(ctx, "SELECT visitor_id FROM flowers WHERE id=?", id).Scan(&visitor); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if visitor == "gardener" {
		return ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM flowers WHERE visitor_id=? AND garden_id=?", visitor, e.state.Garden.ID)
	if err != nil {
		return err
	}
	hiddenIDs := make(map[string]bool)
	for rows.Next() {
		var flowerID string
		if err = rows.Scan(&flowerID); err != nil {
			rows.Close()
			return err
		}
		hiddenIDs[flowerID] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO bans(visitor_id) VALUES (?)", visitor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE flowers SET hidden=1 WHERE visitor_id=?", visitor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE gardens SET revision=revision+1 WHERE active=1"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	e.bans[visitor] = true
	visible := make([]Flower, 0, len(e.state.Flowers))
	for _, flower := range e.state.Flowers {
		if !hiddenIDs[flower.ID] {
			visible = append(visible, flower)
		}
	}
	e.state.Flowers = visible
	e.state.Garden.Revision++
	e.broadcast(e.state)
	return nil
}

func (e *Engine) Hide(ctx context.Context, id string, hidden bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE flowers SET hidden=? WHERE id=?", hidden, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	var shown Flower
	if !hidden {
		shown, err = scanFlower(tx.QueryRowContext(ctx, "SELECT "+flowerColumns+" FROM flowers WHERE id=?", id))
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE gardens SET revision=revision+1 WHERE active=1"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	visible := make([]Flower, 0, len(e.state.Flowers)+1)
	for _, flower := range e.state.Flowers {
		if flower.ID != id {
			visible = append(visible, flower)
		}
	}
	if !hidden && shown.GardenID == e.state.Garden.ID {
		visible = append(visible, shown)
	}
	e.state.Flowers = visible
	e.state.Garden.Revision++
	e.broadcast(e.state)
	return nil
}

func (e *Engine) NewGarden(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM gardens").Scan(&count); err != nil {
		return err
	}
	if count >= MaxGardens {
		return ErrFull
	}
	if _, err = tx.ExecContext(ctx, "UPDATE gardens SET active=0"); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO gardens(created_at) VALUES (?)", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// No fallible reads after commit: the cache and broadcast must reflect the committed state.
	e.state = Snapshot{Type: "snapshot", Garden: Garden{ID: id, Width: WorldWidth, Height: WorldHeight, Capacity: Capacity}, Flowers: []Flower{}}
	e.broadcast(e.state)
	return nil
}
