// SPDX-License-Identifier: AGPL-3.0-only

// Package mcp lets agents the owner has authorized use Wargo: named,
// revocable tokens with an access level, the MCP settings (off by default)
// and the browser API that manages both.
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Level is what an agent may do. Each level includes the ones before it.
type Level string

const (
	LevelRead    Level = "read"
	LevelOperate Level = "operate"
	LevelAdmin   Level = "admin"
)

func (l Level) rank() int {
	switch l {
	case LevelRead:
		return 1
	case LevelOperate:
		return 2
	case LevelAdmin:
		return 3
	}
	return 0
}

// Valid says whether l is one of the three levels.
func (l Level) Valid() bool { return l.rank() > 0 }

// Allows says whether an agent at level l may do what need requires.
func (l Level) Allows(need Level) bool { return l.Valid() && need.Valid() && l.rank() >= need.rank() }

// Agent is a token holder. The token itself is never kept, only its hash.
type Agent struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Level      Level     `json:"level"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	LastIP     string    `json:"lastIp"`
}

const (
	tokenPrefix = "wargo_"
	maxName     = 64
	// touchEvery bounds how often an agent's last use is written.
	touchEvery = time.Minute
)

// Agents is the table of agents.
type Agents struct {
	st *store.Store

	mu      sync.Mutex
	touched map[string]time.Time
}

func NewAgents(st *store.Store) *Agents {
	return &Agents{st: st, touched: map[string]time.Time{}}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxName {
		return "", httpx.BadRequest("The name must be 1 to 64 characters")
	}
	return name, nil
}

func checkLevel(l Level) error {
	if !l.Valid() {
		return httpx.BadRequest("The level must be read, operate or admin")
	}
	return nil
}

// Create adds an agent and returns its token, which is not recoverable
// afterwards.
func (a *Agents) Create(ctx context.Context, name string, level Level) (Agent, string, error) {
	name, err := cleanName(name)
	if err != nil {
		return Agent{}, "", err
	}
	if err := checkLevel(level); err != nil {
		return Agent{}, "", err
	}
	tb, err := randomBytes(32)
	if err != nil {
		return Agent{}, "", err
	}
	ib, err := randomBytes(6)
	if err != nil {
		return Agent{}, "", err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(tb)
	ag := Agent{ID: hex.EncodeToString(ib), Name: name, Level: level, CreatedAt: time.Now().Truncate(time.Second)}
	_, err = a.st.DB.ExecContext(ctx,
		`INSERT INTO mcp_agents(id, name, level, token_hash, created_at) VALUES(?, ?, ?, ?, ?)`,
		ag.ID, ag.Name, string(ag.Level), hashToken(token), ag.CreatedAt.Unix())
	if err != nil {
		return Agent{}, "", err
	}
	return ag, token, nil
}

const agentColumns = `id, name, level, created_at, last_used_at, last_ip`

func scanAgent(s interface{ Scan(...any) error }) (Agent, error) {
	var ag Agent
	var level string
	var created int64
	var used sql.NullInt64
	var ip sql.NullString
	if err := s.Scan(&ag.ID, &ag.Name, &level, &created, &used, &ip); err != nil {
		return Agent{}, err
	}
	ag.Level = Level(level)
	ag.CreatedAt = time.Unix(created, 0)
	if used.Valid {
		ag.LastUsedAt = time.Unix(used.Int64, 0)
	}
	ag.LastIP = ip.String
	return ag, nil
}

// List returns every agent, oldest first.
func (a *Agents) List(ctx context.Context) ([]Agent, error) {
	rows, err := a.st.DB.QueryContext(ctx, `SELECT `+agentColumns+` FROM mcp_agents ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		ag, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ag)
	}
	return out, rows.Err()
}

// ByToken finds the agent that holds token: false when none does, an
// error when the lookup itself failed (which says nothing about the token).
func (a *Agents) ByToken(ctx context.Context, token string) (Agent, bool, error) {
	return a.one(ctx, `token_hash = ?`, hashToken(token))
}

// ByID finds the agent with id: false once it is revoked, an error when
// the lookup itself failed.
func (a *Agents) ByID(ctx context.Context, id string) (Agent, bool, error) {
	return a.one(ctx, `id = ?`, id)
}

func (a *Agents) one(ctx context.Context, where string, arg any) (Agent, bool, error) {
	ag, err := scanAgent(a.st.DB.QueryRowContext(ctx, `SELECT `+agentColumns+` FROM mcp_agents WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	if err != nil {
		return Agent{}, false, err
	}
	return ag, true, nil
}

func (a *Agents) update(ctx context.Context, q string, args ...any) error {
	res, err := a.st.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	return nil
}

// Update changes the name and/or level (nil keeps it). Both are validated
// before anything is written, and applied in one statement, so a bad value
// leaves the agent untouched.
func (a *Agents) Update(ctx context.Context, id string, name *string, level *Level) error {
	var n, l *string
	if name != nil {
		c, err := cleanName(*name)
		if err != nil {
			return err
		}
		n = &c
	}
	if level != nil {
		if err := checkLevel(*level); err != nil {
			return err
		}
		s := string(*level)
		l = &s
	}
	return a.update(ctx, `UPDATE mcp_agents SET name = COALESCE(?, name), level = COALESCE(?, level) WHERE id = ?`, n, l, id)
}

// Revoke deletes the agent, so its token stops working at once.
func (a *Agents) Revoke(ctx context.Context, id string) error {
	if err := a.update(ctx, `DELETE FROM mcp_agents WHERE id = ?`, id); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.touched, id)
	a.mu.Unlock()
	return nil
}

// Touch records that the agent was used from ip, writing at most once a
// minute per agent. A failed write is not an error for the caller.
func (a *Agents) Touch(ctx context.Context, id, ip string) {
	now := time.Now()
	a.mu.Lock()
	if last, ok := a.touched[id]; ok && now.Sub(last) < touchEvery {
		a.mu.Unlock()
		return
	}
	a.touched[id] = now
	a.mu.Unlock()
	_, _ = a.st.DB.ExecContext(ctx, `UPDATE mcp_agents SET last_used_at = ?, last_ip = ? WHERE id = ?`, now.Unix(), ip, id)
}
