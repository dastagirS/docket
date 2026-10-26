package docket

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var assets embed.FS

const (
	requestBytesMax       = 65536
	requestsConcurrentMax = 32
	requestsPerMinuteMax  = 600
)

type Server struct {
	store     *Store
	origin    *url.URL
	slots     chan struct{}
	rateMutex sync.Mutex
	window    time.Time
	requests  int
}

func NewServer(store *Store, publicURL string) (http.Handler, error) {
	origin, err := url.Parse(publicURL)
	if err != nil || origin == nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("public URL must be an http(s) origin without credentials, path, query, or fragment")
	}
	server := &Server{store: store, origin: origin, slots: make(chan struct{}, requestsConcurrentMax), window: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", server.projects)
	mux.HandleFunc("POST /api/projects", server.createProject)
	mux.HandleFunc("GET /api/projects/{project}", server.project)
	mux.HandleFunc("PATCH /api/projects/{project}", server.updateProject)
	mux.HandleFunc("GET /api/projects/{project}/tickets", server.tickets)
	mux.HandleFunc("POST /api/projects/{project}/tickets", server.createTicket)
	mux.HandleFunc("GET /api/projects/{project}/tickets/{ticket}", server.ticket)
	mux.HandleFunc("PATCH /api/projects/{project}/tickets/{ticket}", server.updateTicket)
	mux.HandleFunc("GET /api/projects/{project}/tickets/{ticket}/comments", server.comments)
	mux.HandleFunc("POST /api/projects/{project}/tickets/{ticket}/comments", server.createComment)
	mux.HandleFunc("GET /api/projects/{project}/tickets/{ticket}/history", server.events)
	mux.HandleFunc("GET /api/projects/{project}/events", server.events)
	mux.HandleFunc("GET /healthz", server.health)
	files, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(files)))
	mux.HandleFunc("GET /", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" && !validProjectPath(request.URL.Path) {
			http.NotFound(writer, request)
			return
		}
		page, err := assets.ReadFile("web/index.html")
		if err != nil {
			server.fail(writer, err)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := writer.Write(page); err != nil {
			slog.Warn("page response interrupted")
		}
	})
	return server.protect(mux), nil
}

func validProjectPath(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 || parts[0] != "projects" {
		return false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && id > 0
}

func (server *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Cache-Control", "no-store")
		// Host validation also blocks DNS rebinding. Proxy headers are deliberately not trusted.
		if !strings.EqualFold(request.Host, server.origin.Host) {
			server.fail(writer, &problem{403, "Host does not match configured public URL"})
			return
		}
		if origin := request.Header.Get("Origin"); origin != "" && origin != server.origin.Scheme+"://"+server.origin.Host {
			server.fail(writer, &problem{403, "Cross-origin requests are not allowed"})
			return
		}
		if request.Header.Get("Sec-Fetch-Site") == "cross-site" {
			server.fail(writer, &problem{403, "Cross-site requests are not allowed"})
			return
		}
		server.rateMutex.Lock()
		if time.Since(server.window) >= time.Minute {
			server.window = time.Now()
			server.requests = 0
		}
		if server.requests <= requestsPerMinuteMax {
			server.requests++
		}
		allowed := server.requests <= requestsPerMinuteMax
		server.rateMutex.Unlock()
		if !allowed {
			writer.Header().Set("Retry-After", "60")
			server.fail(writer, &problem{429, "Request limit reached"})
			return
		}
		select {
		case server.slots <- struct{}{}:
			defer func() { <-server.slots }()
		default:
			server.fail(writer, &problem{503, "Server busy; try again later"})
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), queryTimeout)
		defer cancel()
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (server *Server) fail(writer http.ResponseWriter, err error) {
	err = databaseProblem(err)
	failure := &problem{500, "Operation failed. Reload to check the result before trying again."}
	var known *problem
	if errors.As(err, &known) {
		failure = known
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		failure = &problem{503, "Request interrupted or database timed out; reload to check the result"}
	} else {
		slog.Error("request operation failed", "type", "internal")
	}
	server.send(writer, failure.status, map[string]string{"error": failure.message})
}

func (server *Server) send(writer http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, "Response encoding failed", 500)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if _, err := writer.Write(append(body, '\n')); err != nil {
		slog.Warn("JSON response interrupted")
	}
}

func decode(writer http.ResponseWriter, request *http.Request, value any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &problem{415, "Content-Type must be application/json"}
	}
	request.Body = http.MaxBytesReader(writer, request.Body, requestBytesMax)
	body, err := io.ReadAll(request.Body)
	if err != nil || !utf8.Valid(body) {
		return &problem{400, "Request must be valid UTF-8 JSON within 64 KiB"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return &problem{400, "Invalid or oversized JSON request"}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return &problem{400, "Provide one JSON object"}
	}
	return nil
}

func identifiers(request *http.Request) (int64, int64, error) {
	project, err := strconv.ParseInt(request.PathValue("project"), 10, 64)
	if err != nil || project <= 0 {
		return 0, 0, &problem{400, "Invalid project ID"}
	}
	var ticket int64
	if value := request.PathValue("ticket"); value != "" {
		ticket, err = strconv.ParseInt(value, 10, 64)
		if err != nil || ticket <= 0 {
			return 0, 0, &problem{400, "Invalid ticket ID"}
		}
	}
	return project, ticket, nil
}

func page(request *http.Request) (int64, int, error) {
	after := int64(0)
	limit := PageSizeMax
	var err error
	if value := request.URL.Query().Get("after"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 {
			return 0, 0, &problem{400, "Invalid after cursor"}
		}
	}
	if value := request.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > PageSizeMax {
			return 0, 0, &problem{400, "Limit must be between 1 and 100"}
		}
	}
	return after, limit, nil
}

func actor(request *http.Request) (string, error) {
	value := strings.TrimSpace(request.Header.Get("X-Docket-Actor"))
	if value == "" {
		value = "Owner"
	}
	if !validText(value, ActorBytesMax, true) {
		return "", &problem{400, "Invalid actor label"}
	}
	return value, nil
}

func expectedVersion(request *http.Request) (int64, error) {
	value := request.Header.Get("If-Match")
	if value == "" {
		return 0, &problem{428, "If-Match with the current quoted version is required"}
	}
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, &problem{400, "If-Match must be a quoted positive version"}
	}
	version, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || version <= 0 {
		return 0, &problem{400, "Invalid version"}
	}
	return version, nil
}

func (server *Server) health(writer http.ResponseWriter, request *http.Request) {
	if err := server.store.database.PingContext(request.Context()); err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 200, map[string]string{"status": "ok"})
}

func (server *Server) projects(writer http.ResponseWriter, request *http.Request) {
	after, limit, err := page(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	rows, err := server.store.database.QueryContext(request.Context(), "SELECT "+projectColumns+" FROM projects WHERE id>? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		server.fail(writer, err)
		return
	}
	defer rows.Close()
	items := []Project{}
	for rows.Next() {
		project, err := readProject(rows)
		if err != nil {
			server.fail(writer, err)
			return
		}
		items = append(items, project)
		after = project.ID
	}
	if err := rows.Err(); err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 200, map[string]any{"items": items, "next_cursor": after})
}

func (server *Server) project(writer http.ResponseWriter, request *http.Request) {
	projectID, _, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	project, err := readProject(server.store.database.QueryRowContext(request.Context(), "SELECT "+projectColumns+" FROM projects WHERE id=?", projectID))
	if err != nil {
		server.fail(writer, err)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatInt(project.Version, 10)))
	server.send(writer, 200, project)
}

func (server *Server) createProject(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Name     string   `json:"name"`
		Key      string   `json:"key"`
		Statuses []string `json:"statuses"`
	}
	if err := decode(writer, request, &input); err != nil {
		server.fail(writer, err)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Key = strings.ToUpper(strings.TrimSpace(input.Key))
	validKey := len(input.Key) >= 2 && len(input.Key) <= 10
	for _, character := range input.Key {
		if !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			validKey = false
		}
	}
	if !validKey || !validText(input.Name, NameBytesMax, true) {
		server.fail(writer, &problem{400, "Name required (100 bytes max); key must be 2–10 letters/digits"})
		return
	}
	if input.Statuses == nil {
		input.Statuses = defaultStatuses
	}
	if err := validateStatuses(input.Statuses); err != nil {
		server.fail(writer, err)
		return
	}
	label, err := actor(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	value, err := server.store.mutate(request.Context(), 0, nil, "project.created", label, func(transaction *sql.Tx) (any, error) {
		var count int
		if err := transaction.QueryRowContext(request.Context(), "SELECT COUNT(*) FROM projects").Scan(&count); err != nil {
			return nil, err
		}
		if count >= ProjectsMax {
			return nil, capacity
		}
		statuses, err := json.Marshal(input.Statuses)
		if err != nil {
			return nil, err
		}
		createdAt := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := transaction.ExecContext(request.Context(), "INSERT INTO projects(name,key,statuses,created_at) VALUES(?,?,?,?)", input.Name, input.Key, string(statuses), createdAt)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		return Project{ID: id, Name: input.Name, Key: input.Key, Statuses: input.Statuses, Version: 1, CreatedAt: createdAt}, err
	})
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 201, value)
}

func (server *Server) updateProject(writer http.ResponseWriter, request *http.Request) {
	projectID, _, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	version, err := expectedVersion(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	var input struct {
		Name     string   `json:"name"`
		Statuses []string `json:"statuses"`
	}
	if err := decode(writer, request, &input); err != nil {
		server.fail(writer, err)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validText(input.Name, NameBytesMax, true) {
		server.fail(writer, &problem{400, "Project name required (100 bytes max)"})
		return
	}
	if err := validateStatuses(input.Statuses); err != nil {
		server.fail(writer, err)
		return
	}
	label, err := actor(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	value, err := server.store.mutate(request.Context(), projectID, nil, "project.updated", label, func(transaction *sql.Tx) (any, error) {
		project, err := readProject(transaction.QueryRowContext(request.Context(), "SELECT "+projectColumns+" FROM projects WHERE id=?", projectID))
		if err != nil {
			return nil, err
		}
		if project.Version != version {
			return nil, conflict
		}
		rows, err := transaction.QueryContext(request.Context(), "SELECT DISTINCT status FROM tickets WHERE project_id=? LIMIT ?", projectID, StatusesMax)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var status string
			if err := rows.Scan(&status); err != nil {
				rows.Close()
				return nil, err
			}
			if !slices.Contains(input.Statuses, status) {
				rows.Close()
				return nil, &problem{409, "Move tickets before removing their status"}
			}
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return nil, err
		}
		statuses, err := json.Marshal(input.Statuses)
		if err != nil {
			return nil, err
		}
		if _, err := transaction.ExecContext(request.Context(), "UPDATE projects SET name=?,statuses=?,version=version+1 WHERE id=?", input.Name, string(statuses), projectID); err != nil {
			return nil, err
		}
		project.Name = input.Name
		project.Statuses = input.Statuses
		project.Version++
		return project, nil
	})
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 200, value)
}

func (server *Server) tickets(writer http.ResponseWriter, request *http.Request) {
	projectID, _, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	after, limit, err := page(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	search := request.URL.Query().Get("q")
	if !validText(search, TitleBytesMax, false) {
		server.fail(writer, &problem{400, "Search is too long"})
		return
	}
	if _, err := readProject(server.store.database.QueryRowContext(request.Context(), "SELECT "+projectColumns+" FROM projects WHERE id=?", projectID)); err != nil {
		server.fail(writer, err)
		return
	}
	rows, err := server.store.database.QueryContext(request.Context(), "SELECT "+ticketColumns+" FROM tickets WHERE project_id=? AND id>? AND (?='' OR instr(lower(title),lower(?))>0) ORDER BY id LIMIT ?", projectID, after, search, search, limit)
	if err != nil {
		server.fail(writer, err)
		return
	}
	defer rows.Close()
	items := []Ticket{}
	for rows.Next() {
		ticket, err := readTicket(rows)
		if err != nil {
			server.fail(writer, err)
			return
		}
		items = append(items, ticket)
		after = ticket.ID
	}
	if err := rows.Err(); err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 200, map[string]any{"items": items, "next_cursor": after})
}

func (server *Server) ticket(writer http.ResponseWriter, request *http.Request) {
	projectID, ticketID, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	ticket, err := readTicket(server.store.database.QueryRowContext(request.Context(), "SELECT "+ticketColumns+" FROM tickets WHERE project_id=? AND id=?", projectID, ticketID))
	if err != nil {
		server.fail(writer, err)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatInt(ticket.Version, 10)))
	server.send(writer, 200, ticket)
}

type ticketInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

func validateTicket(input ticketInput) error {
	if input.Title != nil && !validText(*input.Title, TitleBytesMax, true) {
		return &problem{400, "Title required (200 bytes max)"}
	}
	if input.Description != nil && !validText(*input.Description, TextBytesMax, false) {
		return &problem{400, "Description exceeds 16000 bytes"}
	}
	if input.Status != nil && !validText(*input.Status, NameBytesMax, true) {
		return &problem{400, "Invalid status"}
	}
	return nil
}

func (server *Server) createTicket(writer http.ResponseWriter, request *http.Request) {
	projectID, _, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	var input ticketInput
	if err := decode(writer, request, &input); err != nil {
		server.fail(writer, err)
		return
	}
	if input.Title == nil {
		server.fail(writer, &problem{400, "Title is required"})
		return
	}
	if err := validateTicket(input); err != nil {
		server.fail(writer, err)
		return
	}
	label, err := actor(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	value, err := server.store.mutate(request.Context(), projectID, nil, "ticket.created", label, func(transaction *sql.Tx) (any, error) {
		project, err := readProject(transaction.QueryRowContext(request.Context(), "SELECT "+projectColumns+" FROM projects WHERE id=?", projectID))
		if err != nil {
			return nil, err
		}
		status := project.Statuses[0]
		if input.Status != nil {
			status = *input.Status
		}
		if !slices.Contains(project.Statuses, status) {
			return nil, &problem{400, "Status does not belong to this project"}
		}
		var count int
		if err := transaction.QueryRowContext(request.Context(), "SELECT COUNT(*) FROM tickets").Scan(&count); err != nil {
			return nil, err
		}
		if count >= TicketsMax {
			return nil, capacity
		}
		description := ""
		if input.Description != nil {
			description = *input.Description
		}
		createdAt := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := transaction.ExecContext(request.Context(), "INSERT INTO tickets(project_id,title,description,status,created_at,updated_at) VALUES(?,?,?,?,?,?)", projectID, strings.TrimSpace(*input.Title), description, status, createdAt, createdAt)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		return Ticket{ID: id, ProjectID: projectID, Title: strings.TrimSpace(*input.Title), Description: description, Status: status, Version: 1, CreatedAt: createdAt, UpdatedAt: createdAt}, err
	})
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 201, value)
}

func (server *Server) updateTicket(writer http.ResponseWriter, request *http.Request) {
	projectID, ticketID, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	version, err := expectedVersion(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	var input ticketInput
	if err := decode(writer, request, &input); err != nil {
		server.fail(writer, err)
		return
	}
	if input.Title == nil && input.Description == nil && input.Status == nil {
		server.fail(writer, &problem{400, "No ticket fields supplied"})
		return
	}
	if err := validateTicket(input); err != nil {
		server.fail(writer, err)
		return
	}
	label, err := actor(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	value, err := server.store.mutate(request.Context(), projectID, &ticketID, "ticket.updated", label, func(transaction *sql.Tx) (any, error) {
		ticket, err := readTicket(transaction.QueryRowContext(request.Context(), "SELECT "+ticketColumns+" FROM tickets WHERE project_id=? AND id=?", projectID, ticketID))
		if err != nil {
			return nil, err
		}
		if ticket.Version != version {
			return nil, conflict
		}
		if input.Title != nil {
			ticket.Title = strings.TrimSpace(*input.Title)
		}
		if input.Description != nil {
			ticket.Description = *input.Description
		}
		if input.Status != nil {
			project, err := readProject(transaction.QueryRowContext(request.Context(), "SELECT "+projectColumns+" FROM projects WHERE id=?", projectID))
			if err != nil {
				return nil, err
			}
			if !slices.Contains(project.Statuses, *input.Status) {
				return nil, &problem{400, "Status does not belong to this project"}
			}
			ticket.Status = *input.Status
		}
		ticket.Version++
		ticket.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_, err = transaction.ExecContext(request.Context(), "UPDATE tickets SET title=?,description=?,status=?,version=?,updated_at=? WHERE project_id=? AND id=?", ticket.Title, ticket.Description, ticket.Status, ticket.Version, ticket.UpdatedAt, projectID, ticketID)
		return ticket, err
	})
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 200, value)
}

func (server *Server) comments(writer http.ResponseWriter, request *http.Request) {
	projectID, ticketID, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	after, limit, err := page(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	if _, err := readTicket(server.store.database.QueryRowContext(request.Context(), "SELECT "+ticketColumns+" FROM tickets WHERE project_id=? AND id=?", projectID, ticketID)); err != nil {
		server.fail(writer, err)
		return
	}
	rows, err := server.store.database.QueryContext(request.Context(), "SELECT id,ticket_id,body,actor,created_at FROM comments WHERE ticket_id=? AND id>? ORDER BY id LIMIT ?", ticketID, after, limit)
	if err != nil {
		server.fail(writer, err)
		return
	}
	defer rows.Close()
	items := []Comment{}
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(&comment.ID, &comment.TicketID, &comment.Body, &comment.Actor, &comment.CreatedAt); err != nil {
			server.fail(writer, err)
			return
		}
		items = append(items, comment)
		after = comment.ID
	}
	if err := rows.Err(); err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 200, map[string]any{"items": items, "next_cursor": after})
}

func (server *Server) createComment(writer http.ResponseWriter, request *http.Request) {
	projectID, ticketID, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	version, err := expectedVersion(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	var input struct {
		Body string `json:"body"`
	}
	if err := decode(writer, request, &input); err != nil {
		server.fail(writer, err)
		return
	}
	if !validText(input.Body, TextBytesMax, true) {
		server.fail(writer, &problem{400, "Comment required (16000 bytes max)"})
		return
	}
	label, err := actor(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	value, err := server.store.mutate(request.Context(), projectID, &ticketID, "comment.created", label, func(transaction *sql.Tx) (any, error) {
		ticket, err := readTicket(transaction.QueryRowContext(request.Context(), "SELECT "+ticketColumns+" FROM tickets WHERE project_id=? AND id=?", projectID, ticketID))
		if err != nil {
			return nil, err
		}
		if ticket.Version != version {
			return nil, conflict
		}
		createdAt := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := transaction.ExecContext(request.Context(), "INSERT INTO comments(ticket_id,body,actor,created_at) VALUES(?,?,?,?)", ticketID, input.Body, label, createdAt)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		if _, err := transaction.ExecContext(request.Context(), "UPDATE tickets SET version=version+1,updated_at=? WHERE id=? AND project_id=?", createdAt, ticketID, projectID); err != nil {
			return nil, err
		}
		return map[string]any{"comment": Comment{ID: id, TicketID: ticketID, Body: input.Body, Actor: label, CreatedAt: createdAt}, "ticket_version": version + 1}, nil
	})
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 201, value)
}

func (server *Server) events(writer http.ResponseWriter, request *http.Request) {
	projectID, ticketID, err := identifiers(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	after, limit, err := page(request)
	if err != nil {
		server.fail(writer, err)
		return
	}
	if ticketID != 0 {
		if _, err := readTicket(server.store.database.QueryRowContext(request.Context(), "SELECT "+ticketColumns+" FROM tickets WHERE project_id=? AND id=?", projectID, ticketID)); err != nil {
			server.fail(writer, err)
			return
		}
	} else {
		if _, err := readProject(server.store.database.QueryRowContext(request.Context(), "SELECT "+projectColumns+" FROM projects WHERE id=?", projectID)); err != nil {
			server.fail(writer, err)
			return
		}
	}
	rows, err := server.store.database.QueryContext(request.Context(), "SELECT id,project_id,ticket_id,kind,actor,data,created_at FROM events WHERE project_id=? AND (?=0 OR ticket_id=?) AND id>? ORDER BY id LIMIT ?", projectID, ticketID, ticketID, after, limit)
	if err != nil {
		server.fail(writer, err)
		return
	}
	defer rows.Close()
	items := []Event{}
	for rows.Next() {
		var event Event
		var data string
		if err := rows.Scan(&event.ID, &event.ProjectID, &event.TicketID, &event.Kind, &event.Actor, &data, &event.CreatedAt); err != nil {
			server.fail(writer, err)
			return
		}
		event.Data = json.RawMessage(data)
		items = append(items, event)
		after = event.ID
	}
	if err := rows.Err(); err != nil {
		server.fail(writer, err)
		return
	}
	server.send(writer, 200, map[string]any{"items": items, "next_cursor": after})
}
