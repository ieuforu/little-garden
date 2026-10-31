package garden

import "errors"

const (
	WorldWidth  = 2400
	WorldHeight = 1600
	Capacity    = 800
	Spacing     = 70
	MaxGardens  = 30
)

var (
	ErrFull     = errors.New("garden_full")
	ErrPaused   = errors.New("garden_paused")
	ErrSpacing  = errors.New("too_close")
	ErrInvalid  = errors.New("invalid_flower")
	ErrStale    = errors.New("garden_changed")
	ErrConflict = errors.New("request_id_conflict")
	ErrBanned   = errors.New("session_blocked")
	ErrBusy     = errors.New("garden_busy")
	ErrNotFound = errors.New("flower_not_found")
)

type Garden struct {
	ID       int64 `json:"id"`
	Width    int   `json:"width"`
	Height   int   `json:"height"`
	Capacity int   `json:"capacity"`
	Revision int64 `json:"revision"`
	Paused   bool  `json:"paused"`
	Planted  int   `json:"planted"` // Includes moderated flowers: hiding never restores the write budget.
}

type Flower struct {
	ID           string  `json:"id"`
	GardenID     int64   `json:"gardenId"`
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	Seed         uint32  `json:"seed"`
	Palette      int     `json:"palette"`
	StyleVersion int     `json:"styleVersion"`
	CreatedAt    string  `json:"createdAt"`
}

type Snapshot struct {
	Type    string   `json:"type"`
	Garden  Garden   `json:"garden"`
	Flowers []Flower `json:"flowers"`
}

type PlantRequest struct {
	GardenID  int64   `json:"gardenId"`
	RequestID string  `json:"requestId"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Palette   int     `json:"palette"`
}

type PlantResult struct {
	Type      string `json:"type"`
	Revision  int64  `json:"revision"`
	Flower    Flower `json:"flower"`
	Duplicate bool   `json:"duplicate,omitempty"`
}
