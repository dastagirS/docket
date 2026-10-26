package docket

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-sqlite3"
)

const (
	ProjectsMax      = 128
	TicketsMax       = 10000
	EventsMax        = 250000
	PageSizeMax      = 100
	StatusesMax      = 12
	NameBytesMax     = 100
	TitleBytesMax    = 200
	TextBytesMax     = 16000
	ActorBytesMax    = 80
	databasePagesMax = 131072 // 512 MiB at the required 4096-byte page size; never silently discard history.
	queryTimeout     = 5 * time.Second
)

var defaultStatuses = []string{"To Do", "In Progress", "Blocked", "In Review", "Done"}

type Project struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Key       string   `json:"key"`
	Statuses  []string `json:"statuses"`
	Version   int64    `json:"version"`
	CreatedAt string   `json:"created_at"`
}

type Ticket struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Comment struct {
	ID        int64  `json:"id"`
	TicketID  int64  `json:"ticket_id"`
	Body      string `json:"body"`
	Actor     string `json:"actor"`
	CreatedAt string `json:"created_at"`
}

type Event struct {
	ID        int64           `json:"id"`
	ProjectID int64           `json:"project_id"`
	TicketID  *int64          `json:"ticket_id,omitempty"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"created_at"`
}

type problem struct {
	status  int
	message string
}

func (failure *problem) Error() string { return failure.message }

var (
	notFound = &problem{404, "Not found in this project"}
	conflict = &problem{409, "Changed since you loaded it. Reload before trying again."}
	capacity = &problem{503, "Storage limit reached; back up the database and review its limits."}
)

type Store struct{ database *sql.DB }

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, fmt.Errorf("database directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("database file close: %w", err)
	}
	location := url.URL{Scheme: "file", Path: absolute}
	location.RawQuery = "_foreign_keys=on&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=1000&_txlock=immediate"
	database, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, err
	}
	// A single writer serializes SQLite work; request deadlines bound pool waits.
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &Store{database: database}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return store, nil
}

func (store *Store) Close() error { return store.database.Close() }

func (store *Store) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var version, pageSize int
	if err := store.database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("database schema is newer than this executable")
	}
	if err := store.database.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return err
	}
	if pageSize != 4096 {
		return errors.New("unsupported database page size; expected 4096")
	}
	var pagesMax int64
	if err := store.database.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", databasePagesMax)).Scan(&pagesMax); err != nil {
		return err
	}
	if pagesMax > databasePagesMax {
		return errors.New("database already exceeds the supported page limit")
	}
	if version == 1 {
		return nil
	}
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(transaction)
	_, err = transaction.ExecContext(ctx, `
CREATE TABLE projects (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 name TEXT NOT NULL,
 key TEXT NOT NULL UNIQUE,
 statuses TEXT NOT NULL CHECK(json_valid(statuses)),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
 created_at TEXT NOT NULL
);
CREATE TABLE tickets (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id INTEGER NOT NULL REFERENCES projects(id),
 title TEXT NOT NULL,
 description TEXT NOT NULL,
 status TEXT NOT NULL,
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX tickets_project ON tickets(project_id, id);
CREATE TABLE comments (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id),
 body TEXT NOT NULL,
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX comments_ticket ON comments(ticket_id, id);
CREATE TABLE events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id INTEGER NOT NULL REFERENCES projects(id),
 ticket_id INTEGER REFERENCES tickets(id),
 kind TEXT NOT NULL,
 actor TEXT NOT NULL,
 data TEXT NOT NULL CHECK(json_valid(data)),
 created_at TEXT NOT NULL
);
CREATE INDEX events_project ON events(project_id, id);
CREATE INDEX events_ticket ON events(ticket_id, id);
PRAGMA user_version=1;`)
	if err != nil {
		return err
	}
	return transaction.Commit()
}

func (store *Store) mutate(ctx context.Context, projectID int64, ticketID *int64, kind, actor string, change func(*sql.Tx) (any, error)) (any, error) {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(transaction)
	value, err := change(transaction)
	if err != nil {
		return nil, err
	}
	if projectID == 0 {
		project, ok := value.(Project)
		if !ok {
			return nil, errors.New("project creation returned invalid event data")
		}
		projectID = project.ID
	}
	if ticket, ok := value.(Ticket); ok && ticketID == nil {
		ticketID = &ticket.ID
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	result, err := transaction.ExecContext(ctx, `INSERT INTO events(project_id,ticket_id,kind,actor,data,created_at)
SELECT ?,?,?,?,?,? WHERE (SELECT COALESCE(MAX(id),0) FROM events) < ?`, projectID, ticketID, kind, actor, string(data), time.Now().UTC().Format(time.RFC3339Nano), EventsMax)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, capacity
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return value, nil
}

func rollback(transaction *sql.Tx) {
	if err := transaction.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.Error("database rollback failed")
	}
}

type rowReader interface{ Scan(...any) error }

func readProject(row rowReader) (Project, error) {
	var project Project
	var statuses string
	if err := row.Scan(&project.ID, &project.Name, &project.Key, &statuses, &project.Version, &project.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return project, notFound
		}
		return project, err
	}
	if err := json.Unmarshal([]byte(statuses), &project.Statuses); err != nil {
		return project, err
	}
	if err := validateStatuses(project.Statuses); err != nil {
		return project, errors.New("invalid stored status configuration")
	}
	return project, nil
}

func readTicket(row rowReader) (Ticket, error) {
	var ticket Ticket
	err := row.Scan(&ticket.ID, &ticket.ProjectID, &ticket.Title, &ticket.Description, &ticket.Status, &ticket.Version, &ticket.CreatedAt, &ticket.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ticket, notFound
	}
	return ticket, err
}

const projectColumns = "id,name,key,statuses,version,created_at"
const ticketColumns = "id,project_id,title,description,status,version,created_at,updated_at"

func validText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, 0) && (!required || strings.TrimSpace(value) != "")
}

func validateStatuses(statuses []string) error {
	if len(statuses) == 0 || len(statuses) > StatusesMax {
		return &problem{400, "Choose between 1 and 12 statuses"}
	}
	seen := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		if !validText(status, NameBytesMax, true) || status != strings.TrimSpace(status) || seen[status] {
			return &problem{400, "Status names must be nonempty, unique, trimmed, and at most 100 bytes"}
		}
		seen[status] = true
	}
	return nil
}

func databaseProblem(err error) error {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) {
		switch sqliteError.Code {
		case sqlite3.ErrConstraint:
			return &problem{409, "That value conflicts with existing data"}
		case sqlite3.ErrBusy, sqlite3.ErrLocked:
			return &problem{503, "Database busy; try again later"}
		case sqlite3.ErrFull:
			return capacity
		}
	}
	return err
}

// VACUUM INTO reads a consistent snapshot, including committed WAL data.
func (store *Store) Backup(ctx context.Context, target string) error {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if _, err := store.database.ExecContext(ctx, "VACUUM INTO ?", absolute); err != nil {
		return errors.Join(err, os.Remove(absolute))
	}
	return nil
}
