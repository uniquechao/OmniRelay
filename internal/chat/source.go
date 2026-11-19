package chat

import (
	"context"
	"errors"
	"time"

	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

var (
	ErrAccessLost    = errors.New("chat source access lost")
	ErrUnavailable   = errors.New("chat source unavailable")
	ErrCursorExpired = errors.New("chat source cursor expired")
	ErrPartial       = errors.New("chat source returned partial coverage")
)

type Group struct {
	ID, Name string
}

type Member struct {
	OpenID, Name string
}

type Page[T any] struct {
	Items []T
	Next  string
}

type HistoryQuery struct {
	ChatID, ThreadID, Cursor string
	Start, End               *time.Time
	Limit                    int
}

// Source uses the application's read scopes. Ordinary readers additionally
// require a current membership or the verified identity of the private peer.
type Source interface {
	Groups(context.Context, string) (Page[Group], error)
	Group(context.Context, string) (Group, error)
	Members(context.Context, string, string) (Page[Member], error)
	History(context.Context, HistoryQuery) (Page[orchestrator.ChatMessage], error)
	Bot(context.Context) (Member, error)
}
