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

	"sentra/internal/proto"
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
CREATE TABLE IF NOT EXISTS pve_sources (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL DEFAULT 'default',
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  token_id TEXT NOT NULL,
  secret TEXT NOT NULL,
  fingerprint TEXT NOT NULL DEFAULT '',
  last_ok INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// миграции колонок: SQLite не умеет ADD COLUMN IF NOT EXISTS
var hostColumns = []struct{ name, def string }{
	{"kind", "TEXT NOT NULL DEFAULT 'agent'"},     // agent, pve-node, pve-qemu, pve-lxc, inet
	{"source_id", "TEXT NOT NULL DEFAULT ''"},     // id подключения Proxmox
	{"ext_id", "TEXT NOT NULL DEFAULT ''"},        // qemu/100, node/pve1
	{"meta", "TEXT NOT NULL DEFAULT '{}'"},        // данные из Proxmox: узел, vmid, статус
	{"maint_until", "INTEGER NOT NULL DEFAULT 0"}, // режим обслуживания до (unix)
	{"maint_comment", "TEXT NOT NULL DEFAULT ''"}, // кто и зачем включил
}

func migrate(db *sql.DB) error {
	have := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(hosts)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		have[name] = true
	}
	rows.Close()
	for _, c := range hostColumns {
		if !have[c.name] {
			if _, err := db.Exec(`ALTER TABLE hosts ADD COLUMN ` + c.name + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS hosts_ext ON hosts(source_id, ext_id) WHERE source_id != ''`)
	return err
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: один писатель, без гонок
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	if _, err := db.Exec(problemsSchema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// ---------- настройки ----------

func (s *Store) Setting(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
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
	Kind        string          `json:"kind"`
	SourceID    string          `json:"source_id,omitempty"`
	Meta        map[string]any  `json:"meta"`
	MaintUntil  int64           `json:"maint_until"`
	MaintNote   string          `json:"maint_comment"`
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
	if err := s.db.QueryRow(`SELECT key_hash FROM hosts WHERE id=? AND kind='agent'`, id).Scan(&h); err != nil {
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
	var meta string
	if err := sc.Scan(&h.ID, &h.Name, &inv, &h.LastSeen, &lm, &h.CreatedAt, &h.Kind, &h.SourceID, &meta, &h.MaintUntil, &h.MaintNote); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(inv), &h.Inventory)
	json.Unmarshal([]byte(lm), &h.LastMetrics)
	json.Unmarshal([]byte(meta), &h.Meta)
	if h.Meta == nil {
		h.Meta = map[string]any{}
	}
	return &h, nil
}

const hostCols = `id, name, inventory, last_seen, last_metrics, created_at, kind, source_id, meta, maint_until, maint_comment`

func (s *Store) Hosts() ([]*Host, error) {
	rows, err := s.db.Query(`SELECT ` + hostCols + ` FROM hosts ORDER BY name COLLATE NOCASE`)
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
	if err == nil {
		s.db.Exec(`DELETE FROM problems WHERE host_id=?`, id)
	}
	return err
}

// ---------- Proxmox ----------

type PVESource struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	URL         string         `json:"url"`
	TokenID     string         `json:"token_id"`
	Secret      string         `json:"-"`
	Fingerprint string         `json:"fingerprint"`
	LastOK      int64          `json:"last_ok"`
	LastError   string         `json:"last_error"`
	Summary     map[string]any `json:"summary"`
	CreatedAt   int64          `json:"created_at"`
}

const pveCols = `id, name, url, token_id, secret, fingerprint, last_ok, last_error, summary, created_at`

func scanPVE(sc interface{ Scan(...any) error }) (*PVESource, error) {
	var p PVESource
	var sum string
	if err := sc.Scan(&p.ID, &p.Name, &p.URL, &p.TokenID, &p.Secret, &p.Fingerprint, &p.LastOK, &p.LastError, &sum, &p.CreatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(sum), &p.Summary)
	return &p, nil
}

func (s *Store) AddPVE(name, url, tokenID, secret, fp string) (string, error) {
	id := "pve" + strings.ToLower(randToken(6))
	_, err := s.db.Exec(`INSERT INTO pve_sources(id, name, url, token_id, secret, fingerprint, created_at) VALUES(?,?,?,?,?,?,?)`,
		id, name, url, tokenID, secret, fp, time.Now().Unix())
	return id, err
}

func (s *Store) PVESources() ([]*PVESource, error) {
	rows, err := s.db.Query(`SELECT ` + pveCols + ` FROM pve_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PVESource{}
	for rows.Next() {
		p, err := scanPVE(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PVESource(id string) (*PVESource, error) {
	return scanPVE(s.db.QueryRow(`SELECT `+pveCols+` FROM pve_sources WHERE id=?`, id))
}

func (s *Store) PVEResult(id string, ok bool, errText string, summary map[string]any) {
	if ok {
		sj, _ := json.Marshal(summary)
		s.db.Exec(`UPDATE pve_sources SET last_ok=?, last_error='', summary=? WHERE id=?`, time.Now().Unix(), string(sj), id)
		return
	}
	s.db.Exec(`UPDATE pve_sources SET last_error=? WHERE id=?`, errText, id)
}

func (s *Store) SetPVEFingerprint(id, fp string) error {
	_, err := s.db.Exec(`UPDATE pve_sources SET fingerprint=?, last_error='' WHERE id=?`, fp, id)
	return err
}

// DeletePVE удаляет подключение вместе с его узлами и машинами.
func (s *Store) DeletePVE(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM problems WHERE host_id IN (SELECT id FROM hosts WHERE source_id=?)`, id)
	tx.Exec(`DELETE FROM hosts WHERE source_id=?`, id)
	tx.Exec(`DELETE FROM pve_sources WHERE id=?`, id)
	return tx.Commit()
}

// UpsertPVEHost создаёт или обновляет узел/машину Proxmox. Имя, которое
// пользователь поменял в панели, не перетирается: обновляем его, только пока
// оно совпадает с прежним именем из Proxmox.
func (s *Store) UpsertPVEHost(sourceID, extID, kind, name string, ts int64, last, meta map[string]any) (string, error) {
	lm, _ := json.Marshal(last)
	mj, _ := json.Marshal(meta)
	var id, oldMeta, curName string
	err := s.db.QueryRow(`SELECT id, meta, name FROM hosts WHERE source_id=? AND ext_id=?`, sourceID, extID).Scan(&id, &oldMeta, &curName)
	if err == sql.ErrNoRows {
		id = strings.ToLower(randToken(9))
		_, err = s.db.Exec(`INSERT INTO hosts(id, name, key_hash, kind, source_id, ext_id, meta, last_seen, last_metrics, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, id, name, "-", kind, sourceID, extID, string(mj), ts, string(lm), time.Now().Unix())
		return id, err
	}
	if err != nil {
		return "", err
	}
	var om map[string]any
	json.Unmarshal([]byte(oldMeta), &om)
	if prev, _ := om["pve_name"].(string); prev == curName && name != "" {
		curName = name
	}
	_, err = s.db.Exec(`UPDATE hosts SET name=?, kind=?, meta=?, last_seen=?, last_metrics=? WHERE id=?`,
		curName, kind, string(mj), ts, string(lm), id)
	return id, err
}

// PVEHostIDs — ext_id → id для всех хостов подключения.
func (s *Store) PVEHostIDs(sourceID string) map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT ext_id, id FROM hosts WHERE source_id=?`, sourceID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e, i string
		rows.Scan(&e, &i)
		out[e] = i
	}
	return out
}

// MarkPVEGone помечает машины, которых больше нет в Proxmox.
// PVEMachinesOn — сколько машин подключения стоит на узле (без удалённых).
func (s *Store) PVEMachinesOn(sourceID, node string) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE source_id=? AND kind IN ('pve-qemu','pve-lxc')
		AND json_extract(meta,'$.node')=? AND COALESCE(json_extract(meta,'$.gone'),0)=0`, sourceID, node).Scan(&n)
	return n
}

func (s *Store) MarkPVEGone(id string) {
	s.db.Exec(`UPDATE hosts SET meta=json_set(meta, '$.gone', 1) WHERE id=?`, id)
}
