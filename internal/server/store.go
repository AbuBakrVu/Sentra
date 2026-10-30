package server

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"dozor/internal/proto"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  login TEXT UNIQUE NOT NULL,
  pass_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'admin',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS enroll_tokens (
  token_hash TEXT PRIMARY KEY,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  uses INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS hosts (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL DEFAULT 'default',
  name TEXT NOT NULL,
  machine_id TEXT,
  key_hash TEXT NOT NULL,
  inventory TEXT NOT NULL DEFAULT '{}',
  last_seen INTEGER NOT NULL DEFAULT 0,
  last_metrics TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS hosts_machine ON hosts(tenant_id, machine_id);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: один писатель, без гонок
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// ---------- секреты ----------

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

const pbkdfIter = 210000

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, _ := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	return "pbkdf2$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
}

func checkPassword(hash, pw string) bool {
	p := strings.Split(hash, "$")
	if len(p) != 3 || p[0] != "pbkdf2" {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[1])
	want, err2 := base64.RawStdEncoding.DecodeString(p[2])
	if err1 != nil || err2 != nil {
		return false
	}
	got, _ := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------- пользователи и сессии ----------

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Role  string `json:"role"`
}

func (s *Store) UserCount() (n int) {
	s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	return
}

func (s *Store) CreateUser(login, pw, role string) error {
	_, err := s.db.Exec(`INSERT INTO users(login, pass_hash, role, created_at) VALUES(?,?,?,?)`,
		login, hashPassword(pw), role, time.Now().Unix())
	return err
}

func (s *Store) SetPassword(id int64, pw string) error {
	_, err := s.db.Exec(`UPDATE users SET pass_hash=? WHERE id=?`, hashPassword(pw), id)
	if err == nil {
		s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id)
	}
	return err
}

func (s *Store) Authenticate(login, pw string) (*User, error) {
	var u User
	var h string
	err := s.db.QueryRow(`SELECT id, login, role, pass_hash FROM users WHERE login=?`, login).Scan(&u.ID, &u.Login, &u.Role, &h)
	if err != nil || !checkPassword(h, pw) {
		return nil, errors.New("неверный логин или пароль")
	}
	return &u, nil
}

const sessionTTL = 30 * 24 * time.Hour

func (s *Store) NewSession(userID int64) (string, error) {
	tok := randToken(32)
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at) VALUES(?,?,?)`,
		sha(tok), userID, time.Now().Add(sessionTTL).Unix())
	return tok, err
}

func (s *Store) SessionUser(tok string) (*User, error) {
	var u User
	err := s.db.QueryRow(`SELECT u.id, u.login, u.role FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires_at>?`, sha(tok), time.Now().Unix()).Scan(&u.ID, &u.Login, &u.Role)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) DeleteSession(tok string) {
	s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, sha(tok))
}

// ---------- токены подключения ----------

const enrollTTL = 24 * time.Hour

func (s *Store) NewEnrollToken() (string, time.Time, error) {
	tok := randToken(18)
	exp := time.Now().Add(enrollTTL)
	s.db.Exec(`DELETE FROM enroll_tokens WHERE expires_at<?`, time.Now().Unix())
	_, err := s.db.Exec(`INSERT INTO enroll_tokens(token_hash, expires_at, created_at) VALUES(?,?,?)`,
		sha(tok), exp.Unix(), time.Now().Unix())
	return tok, exp, err
}

func (s *Store) UseEnrollToken(tok string) bool {
	r, err := s.db.Exec(`UPDATE enroll_tokens SET uses=uses+1 WHERE token_hash=? AND expires_at>?`, sha(tok), time.Now().Unix())
	if err != nil {
		return false
	}
	n, _ := r.RowsAffected()
	return n == 1
}

func (s *Store) EnrollTokenValid(tok string) bool {
	var n int
	s.db.QueryRow(`SELECT count(*) FROM enroll_tokens WHERE token_hash=? AND expires_at>?`, sha(tok), time.Now().Unix()).Scan(&n)
	return n == 1
}

// ---------- хосты ----------

type Host struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Inventory   proto.Inventory `json:"inventory"`
	LastSeen    int64           `json:"last_seen"`
	LastMetrics map[string]any  `json:"last_metrics"`
	CreatedAt   int64           `json:"created_at"`
	keyHash     string
}

// RegisterHost создаёт хост или перевыпускает ключ существующему (по machine-id).
func (s *Store) RegisterHost(inv proto.Inventory) (id, key string, err error) {
	key = randToken(32)
	invJSON, _ := json.Marshal(inv)
	if inv.MachineID != "" {
		err = s.db.QueryRow(`SELECT id FROM hosts WHERE tenant_id='default' AND machine_id=?`, inv.MachineID).Scan(&id)
		if err == nil {
			_, err = s.db.Exec(`UPDATE hosts SET key_hash=?, inventory=? WHERE id=?`, sha(key), string(invJSON), id)
			return
		}
	}
	id = strings.ToLower(randToken(9))
	name := inv.Hostname
	if name == "" {
		name = "host-" + id
	}
	_, err = s.db.Exec(`INSERT INTO hosts(id, name, machine_id, key_hash, inventory, created_at) VALUES(?,?,?,?,?,?)`,
		id, name, inv.MachineID, sha(key), string(invJSON), time.Now().Unix())
	return
}

func (s *Store) CheckHostKey(id, key string) bool {
	var h string
	if err := s.db.QueryRow(`SELECT key_hash FROM hosts WHERE id=?`, id).Scan(&h); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h), []byte(sha(key))) == 1
}

func (s *Store) TouchHost(id string, ts int64, last map[string]any, inv *proto.Inventory) error {
	lm, _ := json.Marshal(last)
	if inv != nil {
		ij, _ := json.Marshal(inv)
		_, err := s.db.Exec(`UPDATE hosts SET last_seen=?, last_metrics=?, inventory=? WHERE id=?`, ts, string(lm), string(ij), id)
		return err
	}
	_, err := s.db.Exec(`UPDATE hosts SET last_seen=?, last_metrics=? WHERE id=?`, ts, string(lm), id)
	return err
}

func scanHost(sc interface{ Scan(...any) error }) (*Host, error) {
	var h Host
	var inv, lm string
	if err := sc.Scan(&h.ID, &h.Name, &inv, &h.LastSeen, &lm, &h.CreatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(inv), &h.Inventory)
	json.Unmarshal([]byte(lm), &h.LastMetrics)
	return &h, nil
}

const hostCols = `id, name, inventory, last_seen, last_metrics, created_at`

func (s *Store) Hosts() ([]*Host, error) {
	rows, err := s.db.Query(`SELECT ` + hostCols + ` FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) Host(id string) (*Host, error) {
	return scanHost(s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE id=?`, id))
}

func (s *Store) RenameHost(id, name string) error {
	_, err := s.db.Exec(`UPDATE hosts SET name=? WHERE id=?`, name, id)
	return err
}

func (s *Store) DeleteHost(id string) error {
	_, err := s.db.Exec(`DELETE FROM hosts WHERE id=?`, id)
	return err
}
