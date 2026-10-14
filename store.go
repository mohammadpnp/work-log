package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type project struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Email string `json:"email"`
}

type commit struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"projectId"`
	Project   string `json:"project"`
	Hash      string `json:"hash"`
	Branch    string `json:"branch"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	Date      string `json:"date"`
	Note      string `json:"note"`
}

func openStore(dir string) (*sql.DB, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "worklog.db")
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS projects (
		id INTEGER PRIMARY KEY, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE, email TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS commits (
		id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id),
		hash TEXT NOT NULL, branch TEXT NOT NULL, subject TEXT NOT NULL, body TEXT NOT NULL,
		author TEXT NOT NULL, committed_at INTEGER NOT NULL, note TEXT NOT NULL DEFAULT '',
		UNIQUE(project_id, hash)
	);
	CREATE INDEX IF NOT EXISTS commits_date ON commits(committed_at);`)
	if err == nil {
		err = os.Chmod(path, 0600)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func projects(db *sql.DB) ([]project, error) {
	rows, err := db.Query("SELECT id, name, path, email FROM projects ORDER BY name, path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []project{}
	for rows.Next() {
		var p project
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.Email); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func listCommits(db *sql.DB, q url.Values, zone *time.Location) ([]commit, error) {
	query := `SELECT c.id, p.id, p.name, c.hash, c.branch, c.subject, c.body, c.author, c.committed_at, c.note
		FROM commits c JOIN projects p ON p.id = c.project_id WHERE 1=1`
	args := []any{}
	switch q.Get("status") {
	case "pending":
		query += " AND c.note = ''"
	case "done":
		query += " AND c.note != ''"
	case "", "all":
	default:
		return nil, fmt.Errorf("وضعیت گزارش معتبر نیست")
	}
	if id := q.Get("project"); id != "" {
		query += " AND p.id = ?"
		args = append(args, id)
	}
	for _, key := range []string{"from", "to"} {
		if raw := q.Get(key); raw != "" {
			date, err := time.ParseInLocation("2006-01-02", raw, zone)
			if err != nil {
				return nil, fmt.Errorf("تاریخ معتبر نیست")
			}
			if key == "from" {
				query += " AND c.committed_at >= ?"
			} else {
				query += " AND c.committed_at < ?"
				date = date.AddDate(0, 0, 1)
			}
			args = append(args, date.Unix())
		}
	}
	if q.Get("from") != "" && q.Get("to") != "" && q.Get("from") > q.Get("to") {
		return nil, fmt.Errorf("شروع بازه باید قبل از پایان باشد")
	}
	query += " ORDER BY c.committed_at DESC, c.id DESC"
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []commit{}
	for rows.Next() {
		var c commit
		var date int64
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Project, &c.Hash, &c.Branch, &c.Subject, &c.Body, &c.Author, &date, &c.Note); err != nil {
			return nil, err
		}
		c.Date = time.Unix(date, 0).In(zone).Format(time.RFC3339)
		list = append(list, c)
	}
	return list, rows.Err()
}

func saveNote(db *sql.DB, id int64, note string) error {
	if len(note) > 16000 {
		return fmt.Errorf("توضیح بیش از حد طولانی است")
	}
	result, err := db.Exec("UPDATE commits SET note = ? WHERE id = ?", strings.TrimSpace(note), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return fmt.Errorf("کامیت پیدا نشد")
	}
	return err
}
