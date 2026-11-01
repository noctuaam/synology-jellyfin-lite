package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Account is an application identity. DSM accounts are intentionally separate.
type Account struct {
	ID           string
	Username     string
	Role         string
	Disabled     bool
	AllowedRoots []string
	Salt         string `json:"-"`
	Hash         string `json:"-"`
}

func (s *Server) initializeAccounts() error {
	var schemaExists, itemCount int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='accounts'").Scan(&schemaExists); err != nil {
		return err
	}
	if schemaExists == 0 {
		if err := s.db.QueryRow("SELECT count(*) FROM nodes").Scan(&itemCount); err != nil {
			return err
		}
		if itemCount > 0 {
			if err := s.backupBeforeAccounts(); err != nil {
				return fmt.Errorf("backup before user migration: %w", err)
			}
			// The personal release used 1000 cards by default. Keep its
			// behavior while new installations use a smaller initial page.
			if _, err := s.db.Exec(`INSERT INTO prefs VALUES('management','{"PageSize":1000}') ON CONFLICT(id) DO NOTHING`); err != nil {
				return err
			}
		}
	}
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS accounts (
 id TEXT PRIMARY KEY, username TEXT NOT NULL, normalized_name TEXT UNIQUE NOT NULL,
 salt TEXT NOT NULL, password_hash TEXT NOT NULL, role TEXT NOT NULL,
 disabled INTEGER NOT NULL DEFAULT 0, allowed_roots TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS account_tokens (
 token TEXT PRIMARY KEY, expires INTEGER NOT NULL, account_id TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS account_tokens_account ON account_tokens(account_id);`)
	if err != nil {
		return err
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM accounts").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	// The original ID, password hash, progress keys and tokens retain their
	// meaning. An upgrade does not require rebuilding the media catalog.
	_, err = s.db.Exec(`INSERT INTO accounts VALUES(?,?,?,?,?,'admin',0,'[]')`,
		userID, s.cred.Username, strings.ToLower(strings.TrimSpace(s.cred.Username)), s.cred.Salt, s.cred.Hash)
	return err
}

func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var account Account
	var roots string
	err := row.Scan(&account.ID, &account.Username, &account.Salt, &account.Hash, &account.Role, &account.Disabled, &roots)
	if err == nil {
		err = json.Unmarshal([]byte(roots), &account.AllowedRoots)
	}
	return account, err
}

const accountColumns = "id,username,salt,password_hash,role,disabled,allowed_roots"

func (s *Server) account(id string) (Account, error) {
	return scanAccount(s.db.QueryRow("SELECT "+accountColumns+" FROM accounts WHERE id=?", id))
}

func (s *Server) accountByName(name string) (Account, error) {
	return scanAccount(s.db.QueryRow("SELECT "+accountColumns+" FROM accounts WHERE normalized_name=?", strings.ToLower(strings.TrimSpace(name))))
}

func (s *Server) accountForToken(r *http.Request) (Account, error) {
	value := token(r)
	if value == "" {
		return Account{}, sql.ErrNoRows
	}
	var id string
	err := s.db.QueryRow("SELECT account_id FROM account_tokens WHERE token=? AND expires>?", value, time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Only pre-migration tokens use the old table. New users never receive
		// tokens an old single-user server could mistake for the owner.
		var exists int
		err = s.db.QueryRow("SELECT count(*) FROM tokens WHERE token=? AND expires>?", value, time.Now().Unix()).Scan(&exists)
		if err == nil && exists == 1 {
			id = userID
		} else {
			return Account{}, sql.ErrNoRows
		}
	}
	if err != nil {
		return Account{}, err
	}
	account, err := s.account(id)
	if account.Disabled {
		return Account{}, sql.ErrNoRows
	}
	return account, err
}

func (s *Server) profileID() string {
	if s.identity != nil {
		return s.identity.ID
	}
	return userID
}

func (s *Server) accountName() string {
	if s.identity != nil {
		return s.identity.Username
	}
	return s.cred.Username
}

func (s *Server) accountRole() string {
	if s.identity != nil {
		return s.identity.Role
	}
	return "admin"
}

func (s *Server) administrator() bool {
	return s.identity != nil && s.identity.Role == "admin" && !s.identity.Disabled
}

func (s *Server) storagePrefix() string {
	if s.profileID() == userID {
		return ""
	}
	return s.profileID() + ":"
}

func (s *Server) storageKey(key string) string { return s.storagePrefix() + key }

func (s *Server) permitsRoot(root string) bool {
	if s.identity == nil || s.administrator() {
		return true
	}
	for _, allowed := range s.identity.AllowedRoots {
		if allowed == root {
			return true
		}
	}
	return false
}

func (s *Server) accountView(id string) (*Server, bool) {
	account, err := s.account(id)
	if err != nil || account.Disabled {
		return nil, false
	}
	return &Server{
		sharedState: s.sharedState,
		identity:    &account,
	}, true
}

func (s *Server) authenticateAccount(name, password string) (Account, bool) {
	account, err := s.accountByName(name)
	if err != nil || account.Disabled {
		return Account{}, false
	}
	hash := passwordHash(password, account.Salt)
	return account, subtle.ConstantTimeCompare([]byte(hash), []byte(account.Hash)) == 1
}

func (s *Server) publicAccounts() []M {
	rows, err := s.db.Query("SELECT id,username FROM accounts WHERE disabled=0 ORDER BY normalized_name")
	if err != nil {
		return []M{}
	}
	defer rows.Close()
	users := []M{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return []M{}
		}
		users = append(users, M{
			"Id":                    id,
			"Name":                  name,
			"ServerId":              s.serverIdentity(),
			"HasPassword":           true,
			"HasConfiguredPassword": true,
		})
	}
	if rows.Err() != nil {
		return []M{}
	}
	return users
}

func (s *Server) backupBeforeAccounts() error {
	directory := filepath.Join(s.cfg.Data, "backups", "before-multiuser-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	// VACUUM INTO includes committed WAL contents. Copying catalog.db alone
	// while WAL is in use would produce an incomplete rollback snapshot.
	if _, err := s.db.Exec("VACUUM INTO ?", filepath.Join(directory, "catalog.db")); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(directory, "catalog.db"), 0600); err != nil {
		return err
	}
	credentials, err := json.MarshalIndent(s.cred, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(directory, "credentials.json"), credentials, 0600); err != nil {
		return err
	}
	configuration, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "configuration.json"), configuration, 0600)
}

type accountChange struct {
	Username     string
	Password     string
	Role         string
	Disabled     bool
	AllowedRoots []string
}

func (s *Server) validateAccountChange(change accountChange, creating bool) error {
	if strings.TrimSpace(change.Username) != change.Username || change.Username == "" || len(change.Username) > 128 || strings.ContainsFunc(change.Username, unicode.IsControl) {
		return errors.New("invalid username")
	}
	if change.Role != "admin" && change.Role != "viewer" {
		return errors.New("role must be admin or viewer")
	}
	if (creating && change.Password == "") || len(change.Password) > 1024 {
		return errors.New("invalid password length")
	}
	for _, allowed := range change.AllowedRoots {
		known := false
		for _, root := range s.cfg.Roots {
			known = known || root.Key == allowed
		}
		if !known {
			return fmt.Errorf("unknown library: %s", allowed)
		}
	}
	return nil
}

func (s *Server) manageAccounts(w http.ResponseWriter, r *http.Request, id string) {
	if !s.administrator() {
		http.Error(w, "Administrator access required", 403)
		return
	}
	if r.Method == "GET" {
		rows, err := s.db.Query("SELECT " + accountColumns + " FROM accounts ORDER BY normalized_name")
		if err != nil {
			http.Error(w, "Accounts unavailable", 500)
			return
		}
		defer rows.Close()
		accounts := []Account{}
		for rows.Next() {
			account, err := scanAccount(rows)
			if err != nil {
				http.Error(w, "Accounts unavailable", 500)
				return
			}
			accounts = append(accounts, account)
		}
		if rows.Err() != nil {
			http.Error(w, "Accounts unavailable", 500)
			return
		}
		writeJSON(w, 200, M{
			"Items":     accounts,
			"Libraries": s.cfg.Roots,
		})
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var change accountChange
	if err := decode(r, &change); err != nil {
		http.Error(w, "Invalid account", 400)
		return
	}
	if err := s.validateAccountChange(change, id == ""); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "Accounts unavailable", 500)
		return
	}
	defer tx.Rollback()
	account := Account{ID: id}
	if id != "" {
		account, err = scanAccount(tx.QueryRow("SELECT "+accountColumns+" FROM accounts WHERE id=?", id))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if account.Role == "admin" && !account.Disabled && (change.Role != "admin" || change.Disabled) {
			var others int
			if err = tx.QueryRow("SELECT count(*) FROM accounts WHERE role='admin' AND disabled=0 AND id<>?", id).Scan(&others); err != nil || others == 0 {
				http.Error(w, "The last administrator must remain enabled", 409)
				return
			}
		}
	} else {
		account.ID = randomHex(16)
	}
	if change.Password != "" {
		account.Salt = randomHex(16)
		account.Hash = passwordHash(change.Password, account.Salt)
	}
	roots, err := json.Marshal(change.AllowedRoots)
	if err != nil {
		http.Error(w, "Invalid libraries", 400)
		return
	}
	_, err = tx.Exec(`INSERT INTO accounts VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
 username=excluded.username,normalized_name=excluded.normalized_name,salt=excluded.salt,
 password_hash=excluded.password_hash,role=excluded.role,disabled=excluded.disabled,allowed_roots=excluded.allowed_roots`,
		account.ID, change.Username, strings.ToLower(change.Username), account.Salt, account.Hash, change.Role, change.Disabled, string(roots))
	if err != nil {
		http.Error(w, "Username is unavailable", 409)
		return
	}
	if change.Password != "" || change.Disabled {
		if _, err = tx.Exec("DELETE FROM account_tokens WHERE account_id=?", account.ID); err != nil {
			http.Error(w, "Cannot revoke sessions", 500)
			return
		}
		if account.ID == userID {
			if _, err = tx.Exec("DELETE FROM tokens"); err != nil {
				http.Error(w, "Cannot revoke legacy sessions", 500)
				return
			}
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Cannot save account", 500)
		return
	}
	writeJSON(w, 200, M{"Id": account.ID})
}

// profileSigningKey changes when a password is reset, revoking old bearer URLs
// without invalidating shared cached JPEG or subtitle bytes.
func (s *Server) profileSigningKey() string {
	hash := s.cred.Hash
	if s.identity != nil {
		hash = s.identity.Hash
	}
	return s.cred.Hash + ":" + hash
}
