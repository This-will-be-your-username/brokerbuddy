package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backup and restore.
//
// A backup is a single SQLite file: a consistent snapshot of the database (VACUUM INTO) plus a
// small backup_meta table that carries the encryption key. The key is included on purpose, so
// the one file is enough to move BrokerBuddy to another computer: the bank tokens and other
// secrets inside it are only readable with that key. It also means the file must be kept private.
//
// Importing replaces everything: every table is emptied and refilled from the backup in one
// transaction, so a failed import leaves the current data untouched. The one choice is the
// Telegram bot: the importer either migrates the backup's bot or keeps this install's own Telegram
// settings. A bot token can only be polled by one install at a time, so keeping the local settings
// lets a copy run beside the original without the two competing for the bot's messages.

const backupMetaTable = "backup_meta"

// exportBackup writes a snapshot of the database, with the encryption key, to a new file at path.
func (a *app) exportBackup(path string) error {
	if _, err := a.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return err
	}
	snap, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer snap.Close()
	if _, err := snap.Exec(`CREATE TABLE ` + backupMetaTable + ` (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	key, err := loadOrCreateKey(a.keyPath)
	if err != nil {
		return err
	}
	for k, v := range map[string]string{"format": "1", "key": key, "app_version": version, "created_at": time.Now().UTC().Format(time.RFC3339)} {
		if _, err := snap.Exec(`INSERT INTO `+backupMetaTable+`(key,value) VALUES(?,?)`, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) exportDownload(w http.ResponseWriter, r *http.Request) {
	tmp, err := os.CreateTemp(filepath.Dir(a.dbPath), "export-*.tmp")
	if err != nil {
		fail(w, err, 500)
		return
	}
	path := tmp.Name()
	tmp.Close()
	os.Remove(path) // VACUUM INTO needs a path that does not exist yet
	defer os.Remove(path)
	if err := a.exportBackup(path); err != nil {
		fail(w, err, 500)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="brokerbuddy-backup-`+time.Now().Format("20060102-150405")+`.db"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// readBackupKey checks that the file is a usable BrokerBuddy backup and returns its encryption key.
func readBackupKey(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	head := make([]byte, 16)
	_, err = io.ReadFull(f, head)
	f.Close()
	if err != nil || !bytes.Equal(head, []byte("SQLite format 3\x00")) {
		return "", errors.New("that file is not a BrokerBuddy backup (not a SQLite database)")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		return "", errors.New("the backup file is damaged (integrity check failed)")
	}
	var key string
	if err := db.QueryRow(`SELECT value FROM ` + backupMetaTable + ` WHERE key='key'`).Scan(&key); err != nil || len(key) < 32 {
		return "", errors.New("that file is not a BrokerBuddy backup (export one from Settings → Backup)")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('transactions','accounts','settings')`).Scan(&n); err != nil || n != 3 {
		return "", errors.New("the backup file is missing BrokerBuddy's tables")
	}
	return key, nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func tableColumns(ctx context.Context, c *sql.Conn, schemaName, table string) ([]string, error) {
	rows, err := c.QueryContext(ctx, `SELECT name FROM pragma_table_info(?, ?)`, table, schemaName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols = append(cols, n)
	}
	return cols, rows.Err()
}

// importOptions are the choices offered on the import form.
type importOptions struct {
	// MigrateTelegram takes the Telegram bot (token, recipient, schedule and question history) from
	// the backup. When false, this install's own Telegram settings and history are kept as they are.
	MigrateTelegram bool
}

// tgSettingsWhere selects the settings rows that belong to the Telegram bot.
const tgSettingsWhere = `key LIKE 'telegram\_%' ESCAPE '\'`

type tgSnapshot struct {
	settings map[string]string // plaintext: the token is decrypted so it can be re-sealed with the new key
	asked    [][3]string       // telegram_asked rows: key, asked_at, outcome
}

// snapshotTelegram reads this install's Telegram settings and history so they survive an import.
func (a *app) snapshotTelegram() (*tgSnapshot, error) {
	snap := &tgSnapshot{settings: map[string]string{}}
	rows, err := a.db.Query(`SELECT key, value FROM settings WHERE ` + tgSettingsWhere)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, err
		}
		snap.settings[k] = v
	}
	rows.Close()
	if enc, ok := snap.settings["telegram_token"]; ok {
		if tok, err := a.vault.open(enc); err == nil {
			snap.settings["telegram_token"] = tok
		} else {
			delete(snap.settings, "telegram_token") // already unreadable here: nothing to keep
		}
	}
	rows, err = a.db.Query(`SELECT key, asked_at, outcome FROM telegram_asked`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			return nil, err
		}
		snap.asked = append(snap.asked, r)
	}
	return snap, rows.Err()
}

// restoreTelegram replaces the imported Telegram settings and history with the snapshot, inside the
// import transaction. The token is sealed with the backup's key, which becomes this install's key.
func restoreTelegram(ctx context.Context, conn *sql.Conn, snap *tgSnapshot, v *vault) error {
	if _, err := conn.ExecContext(ctx, `DELETE FROM main.settings WHERE `+tgSettingsWhere); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM main.telegram_asked`); err != nil {
		return err
	}
	for k, val := range snap.settings {
		if k == "telegram_token" {
			enc, err := v.seal(val)
			if err != nil {
				return err
			}
			val = enc
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO main.settings(key,value) VALUES(?,?)`, k, val); err != nil {
			return err
		}
	}
	for _, r := range snap.asked {
		if _, err := conn.ExecContext(ctx, `INSERT INTO main.telegram_asked(key,asked_at,outcome) VALUES(?,?,?)`, r[0], r[1], r[2]); err != nil {
			return err
		}
	}
	return nil
}

// importBackup replaces all data with the contents of the backup file at path.
func (a *app) importBackup(path string, opt importOptions) (err error) {
	key, err := readBackupKey(path)
	if err != nil {
		return err
	}
	newVaultVal, err := newVault(key)
	if err != nil {
		return err
	}
	var keepTg *tgSnapshot
	if !opt.MigrateTelegram {
		if keepTg, err = a.snapshotTelegram(); err != nil {
			return fmt.Errorf("reading this install's Telegram settings: %w", err)
		}
	}
	// Stage the key file first so that only the final rename can fail after the data is committed.
	keyTmp := a.keyPath + ".import"
	if err := os.WriteFile(keyTmp, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(keyTmp)

	ctx := context.Background()
	conn, err := a.db.Conn(ctx) // the pool has one connection: ATTACH and the transaction must share it
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS bk`, path); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `DETACH DATABASE bk`)
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil { // tables are emptied in any order
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()

	var tables []string
	rows, err := conn.QueryContext(ctx, `SELECT name FROM main.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()

	for _, t := range tables {
		if _, err := conn.ExecContext(ctx, `DELETE FROM main.`+quoteIdent(t)); err != nil {
			return err
		}
	}
	conn.ExecContext(ctx, `DELETE FROM main.sqlite_sequence`)
	for _, t := range tables {
		liveCols, err := tableColumns(ctx, conn, "main", t)
		if err != nil {
			return err
		}
		bkCols, err := tableColumns(ctx, conn, "bk", t)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, c := range bkCols {
			have[c] = true
		}
		var shared []string // an older backup may lack newer columns: they keep their defaults
		for _, c := range liveCols {
			if have[c] {
				shared = append(shared, quoteIdent(c))
			}
		}
		if len(shared) == 0 {
			continue
		}
		list := strings.Join(shared, ",")
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`INSERT INTO main.%s(%s) SELECT %s FROM bk.%s`, quoteIdent(t), list, list, quoteIdent(t))); err != nil {
			return fmt.Errorf("restoring %s: %w", t, err)
		}
	}
	if keepTg != nil {
		if err := restoreTelegram(ctx, conn, keepTg, newVaultVal); err != nil {
			return fmt.Errorf("keeping this install's Telegram bot: %w", err)
		}
	}
	if r, err := conn.QueryContext(ctx, `PRAGMA main.foreign_key_check`); err == nil {
		bad := r.Next()
		r.Close()
		if bad {
			return errors.New("the backup's data is inconsistent (broken references)")
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true

	if err := os.Rename(keyTmp, a.keyPath); err != nil {
		return fmt.Errorf("data restored, but the encryption key file could not be replaced: %w", err)
	}
	a.vault = newVaultVal
	return nil
}

func (a *app) importUploadBackup(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { settingsRedirect(w, r, "backup", msg, true) }
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail("Could not read the upload: " + err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	if r.FormValue("confirm") != "on" {
		fail("Tick the box to confirm that you want to replace all existing data.")
		return
	}
	up, _, err := r.FormFile("file")
	if err != nil {
		fail("Choose a backup file to import.")
		return
	}
	defer up.Close()
	tmp, err := os.CreateTemp(filepath.Dir(a.dbPath), "import-*.tmp")
	if err != nil {
		fail(err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, up)
	tmp.Close()
	if err != nil {
		fail("Could not read the upload: " + err.Error())
		return
	}
	opt := importOptions{MigrateTelegram: r.FormValue("telegram") == "on"}
	if err := a.importBackup(tmp.Name(), opt); err != nil {
		log.Println("import backup:", err)
		fail("Import failed, nothing was changed: " + err.Error())
		return
	}
	a.sess.clear() // the restored settings (including the password) apply; everyone signs in again
	a.reloadPlaid()
	a.tg.Reload()
	if a.net != nil {
		go func() { time.Sleep(400 * time.Millisecond); a.net.Reapply() }()
	}
	if a.hasPassword() {
		http.Redirect(w, r, "/login?next="+url.QueryEscape("/settings?msg=Backup+imported.#backup"), http.StatusSeeOther)
		return
	}
	msg := "Backup imported. All previous data was replaced."
	switch {
	case !opt.MigrateTelegram:
		msg = "Backup imported. All previous data was replaced, apart from this install's Telegram bot settings."
	case a.tg.enabled() && a.tg.configured():
		msg += " The Telegram bot now runs here: stop the old install or switch its bot off, so the two don't compete for messages."
	}
	settingsRedirect(w, r, "backup", msg, false)
}
