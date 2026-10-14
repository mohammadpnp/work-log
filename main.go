package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

//go:embed web/*
var web embed.FS

func main() {
	base, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	dir := flag.String("data-dir", filepath.Join(base, "worklog"), "محل دیتابیس و تنظیمات")
	port := flag.Int("port", 7331, "پورت داشبورد محلی")
	flag.Parse()
	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	command := "serve"
	if flag.NArg() > 0 {
		command = flag.Arg(0)
	}
	if command == "hook" {
		if flag.NArg() < 2 {
			log.Fatal("missing hook name")
		}
		os.Exit(dispatchHook(abs, flag.Arg(1), flag.Args()[2:], ""))
	}
	if command == "custom-hook" {
		if flag.NArg() < 3 {
			log.Fatal("missing hook arguments")
		}
		os.Exit(dispatchHook(abs, flag.Arg(1), flag.Args()[3:], flag.Arg(2)))
	}
	if command == "install-hooks" {
		if err := installHooks(abs); err != nil {
			log.Fatal(err)
		}
		fmt.Println("ثبت خودکار فعال شد؛ هوک‌های قبلی حفظ شدند.")
		return
	}
	if command == "uninstall-hooks" {
		if err := uninstallHooks(abs); err != nil {
			log.Fatal(err)
		}
		fmt.Println("تنظیم قبلی هوک‌های گیت برگردانده شد.")
		return
	}
	if command != "serve" {
		log.Fatal("commands: serve, install-hooks, uninstall-hooks")
	}
	if *port < 1 || *port > 65535 {
		log.Fatal("invalid port")
	}
	db, err := openStore(abs)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	zone, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	jsonReply := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(value); err != nil {
			log.Print(err)
		}
	}
	fail := func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		list, err := projects(db)
		if err != nil {
			fail(w, err)
			return
		}
		var total, pending int
		if err := db.QueryRow("SELECT COUNT(*), COALESCE(SUM(note = ''), 0) FROM commits").Scan(&total, &pending); err != nil {
			fail(w, err)
			return
		}
		current, _ := globalHooksPath()
		jsonReply(w, map[string]any{"projects": list, "total": total, "pending": pending, "documented": total - pending, "hooksEnabled": current == filepath.Join(abs, "hooks"), "today": time.Now().In(zone).Format("2006-01-02")})
	})
	mux.HandleFunc("GET /api/commits", func(w http.ResponseWriter, r *http.Request) {
		list, err := listCommits(db, r.URL.Query(), zone)
		if err != nil {
			fail(w, err)
			return
		}
		jsonReply(w, list)
	})
	mux.HandleFunc("POST /api/notes", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID   int64  `json:"id"`
			Note string `json:"note"`
		}
		if err := readJSON(w, r, &input); err != nil {
			fail(w, err)
			return
		}
		if err := saveNote(db, input.ID, input.Note); err != nil {
			fail(w, err)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path   string `json:"path"`
			Import bool   `json:"import"`
		}
		if err := readJSON(w, r, &input); err != nil {
			fail(w, err)
			return
		}
		p, err := registerProject(db, input.Path)
		if err == nil && input.Import {
			err = importHistory(db, p)
		}
		if err == nil {
			err = attachCustomHook(abs, p.Path)
		}
		if err != nil {
			fail(w, err)
			return
		}
		jsonReply(w, p)
	})
	mux.HandleFunc("POST /api/discover", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Root string `json:"root"`
		}
		if err := readJSON(w, r, &input); err != nil {
			fail(w, err)
			return
		}
		count, problems, err := discover(db, abs, input.Root)
		if err != nil {
			fail(w, err)
			return
		}
		jsonReply(w, map[string]any{"count": count, "warnings": problems})
	})
	mux.HandleFunc("POST /api/hooks", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(w, r, &input); err != nil {
			fail(w, err)
			return
		}
		var err error
		if input.Enabled {
			err = installHooks(abs)
		} else {
			err = uninstallHooks(abs)
		}
		if err != nil {
			fail(w, err)
			return
		}
		warnings := []string{}
		if input.Enabled {
			list, err := projects(db)
			if err != nil {
				fail(w, err)
				return
			}
			for _, p := range list {
				if err := attachCustomHook(abs, p.Path); err != nil {
					warnings = append(warnings, p.Name+": "+err.Error())
				}
			}
		}
		jsonReply(w, map[string]any{"ok": true, "warnings": warnings})
	})
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("status", "done")
		list, err := listCommits(db, q, zone)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="worklog-report.md"`)
		fmt.Fprintf(w, "# گزارش کار\n\nبازه: %s تا %s\n\n", q.Get("from"), q.Get("to"))
		grouped := map[string][]commit{}
		order := []string{}
		for _, c := range list {
			key := strconv.FormatInt(c.ProjectID, 10)
			if _, ok := grouped[key]; !ok {
				order = append(order, key)
			}
			grouped[key] = append(grouped[key], c)
		}
		for _, key := range order {
			items := grouped[key]
			fmt.Fprintf(w, "## %s\n\n", markdownLine(items[0].Project))
			for _, c := range items {
				fmt.Fprintf(w, "### %s — %s\n\nکامیت: `%s` | شاخه: %s\n\n%s\n\n", c.Date[:10], markdownLine(c.Subject), c.Hash[:8], markdownLine(c.Branch), c.Note)
			}
		}
	})
	files, err := fs.Sub(web, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(files)))
	address := "127.0.0.1:" + strconv.Itoa(*port)
	server := &http.Server{Addr: address, Handler: localOnly(mux, *port), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second}
	fmt.Printf("Worklog 🐈  http://%s\nدیتابیس: %s\n", address, filepath.Join(abs, "worklog.db"))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return fmt.Errorf("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid JSON body")
	}
	return nil
}

func localOnly(next http.Handler, port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:"+strconv.Itoa(port) && r.Host != "localhost:"+strconv.Itoa(port) {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func markdownLine(text string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "#", "\\#", "[", "\\[", "]", "\\]", "*", "\\*", "`", "\\`", "<", "&lt;", ">", "&gt;").Replace(text)
}
