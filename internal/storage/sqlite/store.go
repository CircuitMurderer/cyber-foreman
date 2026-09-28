package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"cyber-foreman/internal/domain"
	_ "modernc.org/sqlite"
)

const schemaVersion = 1

type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: absPath}
	if err := store.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(absPath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure sqlite database: %w", err)
	}
	return store, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) configure() error {
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite (%s): %w", statement, err)
		}
	}
	return nil
}

func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin sqlite migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			adapter TEXT NOT NULL,
			command_json BLOB NOT NULL,
			cwd TEXT NOT NULL,
			status TEXT NOT NULL,
			exit_code INTEGER,
			error TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS tasks_created_at_idx ON tasks(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS events (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			version TEXT NOT NULL,
			task_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			type TEXT NOT NULL,
			occurred_at TEXT NOT NULL,
			data_json BLOB
		)`,
		`CREATE INDEX IF NOT EXISTS events_task_sequence_idx ON events(task_id, sequence)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply sqlite schema: %w", err)
		}
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		schemaVersion, encodeTime(time.Now().UTC()),
	); err != nil {
		return fmt.Errorf("record sqlite migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite migration: %w", err)
	}
	return nil
}

func (s *Store) PutTask(task domain.Task) error {
	command, err := json.Marshal(task.Command)
	if err != nil {
		return fmt.Errorf("encode task command: %w", err)
	}
	var exitCode any
	if task.ExitCode != nil {
		exitCode = *task.ExitCode
	}
	_, err = s.db.Exec(`INSERT INTO tasks (
		id, kind, adapter, command_json, cwd, status, exit_code, error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		kind=excluded.kind, adapter=excluded.adapter, command_json=excluded.command_json,
		cwd=excluded.cwd, status=excluded.status, exit_code=excluded.exit_code,
		error=excluded.error, created_at=excluded.created_at, updated_at=excluded.updated_at`,
		task.ID, task.Kind, task.Adapter, command, task.CWD, task.Status, exitCode, task.Error,
		encodeTime(task.CreatedAt), encodeTime(task.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("persist task %s: %w", task.ID, err)
	}
	return nil
}

func (s *Store) DeleteTask(id string) error {
	if _, err := s.db.Exec(`DELETE FROM tasks WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete task %s: %w", id, err)
	}
	return nil
}

func (s *Store) ListTasks() ([]domain.Task, error) {
	rows, err := s.db.Query(`SELECT id, kind, adapter, command_json, cwd, status,
		exit_code, error, created_at, updated_at FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var tasks []domain.Task
	for rows.Next() {
		var task domain.Task
		var command []byte
		var exitCode sql.NullInt64
		var createdAt, updatedAt string
		if err := rows.Scan(&task.ID, &task.Kind, &task.Adapter, &command, &task.CWD, &task.Status,
			&exitCode, &task.Error, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		if err := json.Unmarshal(command, &task.Command); err != nil {
			return nil, fmt.Errorf("decode task %s command: %w", task.ID, err)
		}
		if exitCode.Valid {
			value := int(exitCode.Int64)
			task.ExitCode = &value
		}
		if task.CreatedAt, err = decodeTime(createdAt); err != nil {
			return nil, fmt.Errorf("decode task %s created_at: %w", task.ID, err)
		}
		if task.UpdatedAt, err = decodeTime(updatedAt); err != nil {
			return nil, fmt.Errorf("decode task %s updated_at: %w", task.ID, err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, nil
}

func (s *Store) AppendEvent(event domain.Event) (domain.Event, error) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Version == "" {
		event.Version = "v1"
	}
	var data []byte
	var err error
	if event.Data != nil {
		data, err = json.Marshal(event.Data)
		if err != nil {
			return domain.Event{}, fmt.Errorf("encode event data: %w", err)
		}
	}
	result, err := s.db.Exec(`INSERT INTO events (
		version, task_id, session_id, type, occurred_at, data_json
	) VALUES (?, ?, ?, ?, ?, ?)`, event.Version, event.TaskID, event.SessionID, event.Type,
		encodeTime(event.Timestamp), nullableBytes(data))
	if err != nil {
		return domain.Event{}, fmt.Errorf("append event: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return domain.Event{}, fmt.Errorf("read event sequence: %w", err)
	}
	event.Sequence = uint64(sequence)
	event.ID = fmt.Sprintf("evt-%d", sequence)
	return event, nil
}

func (s *Store) EventsAfter(after uint64, taskID string) ([]domain.Event, error) {
	query := `SELECT sequence, version, task_id, session_id, type, occurred_at, data_json
		FROM events WHERE sequence > ?`
	args := []any{after}
	if taskID != "" {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY sequence ASC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		var event domain.Event
		var occurredAt string
		var data []byte
		if err := rows.Scan(&event.Sequence, &event.Version, &event.TaskID, &event.SessionID,
			&event.Type, &occurredAt, &data); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.ID = fmt.Sprintf("evt-%d", event.Sequence)
		if event.Timestamp, err = decodeTime(occurredAt); err != nil {
			return nil, fmt.Errorf("decode event %d timestamp: %w", event.Sequence, err)
		}
		if event.Data, err = decodeEventData(event.Type, data); err != nil {
			return nil, fmt.Errorf("decode event %d data: %w", event.Sequence, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

func decodeEventData(eventType domain.EventType, data []byte) (any, error) {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	var target any
	switch eventType {
	case domain.EventTaskCreated:
		target = &domain.Task{}
	case domain.EventTaskState:
		target = &domain.TaskStateData{}
	case domain.EventAgentOutput:
		target = &domain.AgentOutputData{}
	case domain.EventAgentExited:
		target = &domain.AgentExitData{}
	case domain.EventAgentSessionUpdate:
		target = &domain.AgentSessionUpdateData{}
	case domain.EventAgentStderr:
		target = &domain.AgentStderrData{}
	case domain.EventAgentDisconnected:
		target = &domain.AgentDisconnectedData{}
	case domain.EventAgentPermission:
		target = &domain.AgentPermissionData{}
	case domain.EventConversationMessage:
		target = &domain.ConversationMessageData{}
	default:
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return nil, err
	}
	switch value := target.(type) {
	case *domain.Task:
		return *value, nil
	case *domain.TaskStateData:
		return *value, nil
	case *domain.AgentOutputData:
		return *value, nil
	case *domain.AgentExitData:
		return *value, nil
	case *domain.AgentSessionUpdateData:
		return *value, nil
	case *domain.AgentStderrData:
		return *value, nil
	case *domain.AgentDisconnectedData:
		return *value, nil
	case *domain.AgentPermissionData:
		return *value, nil
	case *domain.ConversationMessageData:
		return *value, nil
	default:
		return nil, errors.New("unsupported event data type")
	}
}

func encodeTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func decodeTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
