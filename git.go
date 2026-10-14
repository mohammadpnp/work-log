package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func git(path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	all := append([]string{"-C", path}, args...)
	cmd := exec.CommandContext(ctx, "git", all...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return output, nil
}

func gitText(path string, args ...string) (string, error) {
	output, err := git(path, args...)
	return strings.TrimSpace(string(output)), err
}

func registerProject(db *sql.DB, path string) (project, error) {
	var p project
	if strings.TrimSpace(path) == "" {
		return p, fmt.Errorf("مسیر پروژه را وارد کن")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		path = filepath.Join(home, path[2:])
	}
	root, err := gitText(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return p, fmt.Errorf("این مسیر پروژهٔ گیت نیست: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return p, err
	}
	common, err := gitText(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return p, err
	}
	// Worktrees share one project and one commit identity.
	if filepath.Base(common) == ".git" {
		root = filepath.Dir(common)
	}
	email, err := gitText(path, "config", "user.email")
	if err != nil || email == "" {
		return p, fmt.Errorf("ابتدا user.email را برای این پروژه در گیت تنظیم کن")
	}
	p.Path, p.Name, p.Email = root, filepath.Base(root), email
	_, err = db.Exec(`INSERT INTO projects(name,path,email) VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET email=excluded.email`, p.Name, p.Path, p.Email)
	if err != nil {
		return p, err
	}
	err = db.QueryRow("SELECT id,name,path,email FROM projects WHERE path=?", root).Scan(&p.ID, &p.Name, &p.Path, &p.Email)
	return p, err
}

const logFormat = "%H%x00%ae%x00%an%x00%cI%x00%s%x00%b"

func storeGitLog(db *sql.DB, p project, data []byte, branch string) error {
	fields := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if len(data) == 0 {
		return nil
	}
	if len(fields)%6 != 0 {
		return fmt.Errorf("قالب خروجی گیت معتبر نیست")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := 0; i < len(fields); i += 6 {
		if !strings.EqualFold(fields[i+1], p.Email) {
			continue
		}
		date, err := time.Parse(time.RFC3339, fields[i+3])
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO commits(project_id,hash,branch,subject,body,author,committed_at)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,hash) DO NOTHING`, p.ID, fields[i], branch, fields[i+4], strings.TrimSpace(fields[i+5]), fields[i+2], date.Unix())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func importHistory(db *sql.DB, p project) error {
	if _, err := gitText(p.Path, "rev-parse", "--verify", "HEAD"); err != nil {
		return nil
	}
	output, err := git(p.Path, "log", "--all", "--since=90 days ago", "-z", "--format="+logFormat)
	if err != nil {
		return err
	}
	return storeGitLog(db, p, output, "")
}

func discover(db *sql.DB, dir, root string) (int, []string, error) {
	if strings.TrimSpace(root) == "" {
		return 0, nil, fmt.Errorf("مسیر پوشه را وارد کن")
	}
	if strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return 0, nil, err
		}
		root = filepath.Join(home, root[2:])
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return 0, nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return 0, nil, err
	}
	if !info.IsDir() {
		return 0, nil, fmt.Errorf("مسیر باید یک پوشه باشد")
	}
	count := 0
	warnings := []string{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, path+": "+walkErr.Error())
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "pkg", ".cache", ".local", ".config":
				return filepath.SkipDir
			}
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			return nil
		}
		p, err := registerProject(db, path)
		if err == nil {
			err = importHistory(db, p)
		}
		if err == nil {
			err = attachCustomHook(dir, path)
		}
		if err != nil {
			warnings = append(warnings, path+": "+err.Error())
		} else {
			count++
		}
		return filepath.SkipDir
	})
	return count, warnings, err
}

type customHook struct {
	Path        string `json:"path"`
	HadOriginal bool   `json:"hadOriginal"`
}

type hookConfig struct {
	Enabled     bool         `json:"enabled"`
	Previous    string       `json:"previous"`
	HadPrevious bool         `json:"hadPrevious"`
	Custom      []customHook `json:"custom"`
}

func readHookConfig(dir string) (hookConfig, error) {
	var cfg hookConfig
	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

func writeHookConfig(dir string, cfg hookConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "hooks.json"), data, 0600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".worklog-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func hookLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "hooks.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func globalHooksPath() (string, error) {
	return gitText(".", "config", "--global", "--get", "core.hooksPath")
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func hookScript(dir, name, original string) []byte {
	command := "hook " + shellQuote(name)
	if original != "" {
		command = "custom-hook " + shellQuote(name) + " " + shellQuote(original)
	}
	return []byte("#!/bin/sh\n# worklog-managed: " + dir + "\nexec " + shellQuote(filepath.Join(dir, "bin", "worklog")) + " --data-dir " + shellQuote(dir) + " " + command + " \"$@\"\n")
}

var hookNames = []string{"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit", "prepare-commit-msg", "commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge", "pre-push", "pre-receive", "update", "proc-receive", "post-receive", "post-update", "reference-transaction", "push-to-checkout", "pre-auto-gc", "post-rewrite", "sendemail-validate", "fsmonitor-watchman", "p4-changelist", "p4-prepare-changelist", "p4-post-changelist", "p4-pre-submit", "post-index-change"}

func installHooks(dir string) error {
	lock, err := hookLock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := readHookConfig(dir)
	if err != nil {
		return err
	}
	current, currentErr := globalHooksPath()
	if currentErr != nil {
		var exit *exec.ExitError
		if !errors.As(currentErr, &exit) || exit.ExitCode() != 1 {
			return currentErr
		}
	}
	hooks := filepath.Join(dir, "hooks")
	if current != hooks {
		cfg.Previous = current
		cfg.HadPrevious = currentErr == nil
	} else if !cfg.Enabled {
		return fmt.Errorf("تنظیم هوک‌ها با وضعیت برنامه سازگار نیست")
	}
	if err = os.MkdirAll(filepath.Join(dir, "bin"), 0700); err != nil {
		return err
	}
	if err = os.MkdirAll(hooks, 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, "bin", "worklog"), data, 0700); err != nil {
		return err
	}
	for _, name := range hookNames {
		if err = atomicWrite(filepath.Join(hooks, name), hookScript(dir, name, ""), 0700); err != nil {
			return err
		}
	}
	cfg.Enabled = true
	if err = writeHookConfig(dir, cfg); err != nil {
		return err
	}
	_, err = gitText(".", "config", "--global", "core.hooksPath", hooks)
	if err != nil {
		cfg.Enabled = false
		_ = writeHookConfig(dir, cfg)
	}
	return err
}

func attachCustomHook(dir, path string) error {
	lock, err := hookLock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := readHookConfig(dir)
	if err != nil || !cfg.Enabled {
		return err
	}
	resolved, err := gitText(path, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return err
	}
	if resolved == filepath.Join(dir, "hooks") {
		return nil
	}
	if err = os.MkdirAll(resolved, 0700); err != nil {
		return err
	}
	for _, name := range []string{"post-commit", "post-rewrite", "post-merge"} {
		hook := filepath.Join(resolved, name)
		old, readErr := os.ReadFile(hook)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if bytes.Equal(old, hookScript(dir, name, hook+".worklog-original")) {
			continue
		}
		if _, err = os.Lstat(hook + ".worklog-original"); err == nil {
			return fmt.Errorf("نسخهٔ پشتیبان هوک موجود است: %s", hook)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, saved := range cfg.Custom {
			if saved.Path == hook {
				return fmt.Errorf("هوک پس از نصب تغییر کرده است: %s", hook)
			}
		}
		hadOriginal := readErr == nil
		cfg.Custom = append(cfg.Custom, customHook{Path: hook, HadOriginal: hadOriginal})
		if err = writeHookConfig(dir, cfg); err != nil {
			return err
		}
		if hadOriginal {
			if err = os.Rename(hook, hook+".worklog-original"); err != nil {
				return err
			}
		}
		if err = atomicWrite(hook, hookScript(dir, name, hook+".worklog-original"), 0700); err != nil {
			if hadOriginal {
				_ = os.Rename(hook+".worklog-original", hook)
			}
			return err
		}
	}
	return nil
}

func uninstallHooks(dir string) error {
	lock, err := hookLock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := readHookConfig(dir)
	if err != nil {
		return err
	}
	current, _ := globalHooksPath()
	if current == filepath.Join(dir, "hooks") {
		if cfg.HadPrevious {
			_, err = gitText(".", "config", "--global", "core.hooksPath", cfg.Previous)
		} else {
			_, err = gitText(".", "config", "--global", "--unset", "core.hooksPath")
		}
		if err != nil {
			return err
		}
	}
	remaining := []customHook{}
	for _, saved := range cfg.Custom {
		data, readErr := os.ReadFile(saved.Path)
		if readErr != nil || !bytes.Equal(data, hookScript(dir, filepath.Base(saved.Path), saved.Path+".worklog-original")) {
			remaining = append(remaining, saved)
			continue
		}
		if saved.HadOriginal {
			err = os.Rename(saved.Path+".worklog-original", saved.Path)
		} else {
			err = os.Remove(saved.Path)
		}
		if err != nil {
			return err
		}
	}
	cfg.Enabled = false
	cfg.Custom = remaining
	return writeHookConfig(dir, cfg)
}

func dispatchHook(dir, name string, args []string, original string) int {
	cfg, err := readHookConfig(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "worklog:", err)
		return 1
	}
	if original == "" {
		if cfg.HadPrevious {
			previous := cfg.Previous
			if strings.HasPrefix(previous, "~/") {
				home, err := os.UserHomeDir()
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
				previous = filepath.Join(home, previous[2:])
			}
			original = filepath.Join(previous, name)
		} else {
			common, err := gitText(".", "rev-parse", "--git-common-dir")
			if err != nil {
				fmt.Fprintln(os.Stderr, "worklog:", err)
				return 1
			}
			original = filepath.Join(common, "hooks", name)
		}
	}
	// Relative hook directories are resolved against Git's hook working directory.
	original, err = filepath.Abs(original)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	input := io.Reader(os.Stdin)
	var rewrite []byte
	if name == "post-rewrite" {
		rewrite, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "worklog:", err)
			return 1
		}
		input = bytes.NewReader(rewrite)
	}
	code := 0
	info, statErr := os.Stat(original)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, statErr)
		return 1
	}
	if statErr == nil && info.Mode()&0111 != 0 {
		cmd := exec.Command(original, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = input, os.Stdout, os.Stderr
		if err = cmd.Run(); err != nil {
			var exit *exec.ExitError
			code = 1
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}
	if cfg.Enabled && (name == "post-commit" || name == "post-rewrite" || name == "post-merge") {
		if err := recordCommit(dir, rewrite); err != nil {
			fmt.Fprintln(os.Stderr, "worklog: ثبت انجام نشد:", err)
		}
	}
	return code
}

func recordCommit(dir string, rewrite []byte) error {
	db, err := openStore(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	p, err := registerProject(db, ".")
	if err != nil {
		return err
	}
	branch, _ := gitText(".", "symbolic-ref", "--short", "-q", "HEAD")
	if branch == "" {
		branch = "detached HEAD"
	}
	data, err := git(".", "log", "-1", "-z", "--format="+logFormat, "HEAD")
	if err != nil {
		return err
	}
	if err = storeGitLog(db, p, data, branch); err != nil {
		return err
	}
	for _, line := range strings.Split(string(rewrite), "\n") {
		pair := strings.Fields(line)
		if len(pair) < 2 || pair[0] == pair[1] {
			continue
		}
		data, err := git(".", "log", "-1", "-z", "--format="+logFormat, pair[1])
		if err != nil {
			return err
		}
		if err = storeGitLog(db, p, data, branch); err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		var oldNote string
		err = tx.QueryRow("SELECT note FROM commits WHERE project_id=? AND hash=?", p.ID, pair[0]).Scan(&oldNote)
		if errors.Is(err, sql.ErrNoRows) {
			tx.Rollback()
			continue
		}
		if err == nil && oldNote != "" {
			// Squashing several commits must preserve every original note.
			_, err = tx.Exec(`UPDATE commits SET note=CASE WHEN note='' THEN ? WHEN note=? THEN note ELSE note || char(10) || char(10) || ? END WHERE project_id=? AND hash=?`, oldNote, oldNote, oldNote, p.ID, pair[1])
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM commits WHERE project_id=? AND hash=? AND EXISTS(SELECT 1 FROM commits WHERE project_id=? AND hash=?)`, p.ID, pair[0], p.ID, pair[1])
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
