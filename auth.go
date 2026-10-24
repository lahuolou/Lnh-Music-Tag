package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	cstore "lnh-music-tag/internal/store"
)

const (
	sessionCookie = "lnh_session"
	sessionTTL    = 12 * time.Hour
	passPrefix    = "sha256$" // 存储格式：sha256$salt$hash（拖库后不可逆）
)

// sessionStore holds active login sessions. The admin credentials live in the
// SQLite config store (no environment variables), so a Settings change takes
// effect immediately.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
	cfg      *cstore.Config
	user     string
	guard    *loginGuard
}

func newSessionStore(cfg *cstore.Config) *sessionStore {
	return &sessionStore{
		sessions: map[string]time.Time{},
		cfg:      cfg,
		user:     cfg.GetDefault("admin_user", "admin"),
		guard:    newLoginGuard(),
	}
}

// ---- 密码存储：加盐 SHA-256（标准库，零依赖），兼容旧明文自动迁移 ----

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	sum := sha256.Sum256(append(append([]byte(nil), salt...), []byte(pw)...))
	return passPrefix + hex.EncodeToString(salt) + "$" + hex.EncodeToString(sum[:])
}

func verifyPassword(stored, pw string) bool {
	if strings.HasPrefix(stored, passPrefix) {
		parts := strings.SplitN(stored, "$", 3)
		if len(parts) != 3 {
			return false
		}
		salt, err := hex.DecodeString(parts[1])
		if err != nil {
			return false
		}
		sum := sha256.Sum256(append(salt, []byte(pw)...))
		want, err := hex.DecodeString(parts[2])
		if err != nil {
			return false
		}
		return subtle.ConstantTimeCompare(sum[:], want) == 1
	}
	// 旧版明文存储：constant-time 比较；调用方在成功后调用 upgradePassword 迁移为哈希
	return subtle.ConstantTimeCompare([]byte(stored), []byte(pw)) == 1
}

// upgradePassword migrates a legacy plaintext password to a salted hash.
func (s *sessionStore) upgradePassword(r *http.Request, newPass string) {
	if err := s.cfg.Set("admin_pass", hashPassword(newPass)); err == nil {
		s.guard.success(r)
	}
}

// bootstrapAdmin ensures an admin account exists in the database. On first run
// it seeds from optional env vars; otherwise it generates a random password.
// Returns (user, pass, generated, error).
func bootstrapAdmin(cfg *cstore.Config, envUser, envPass string) (string, string, bool, error) {
	if pw, ok := cfg.Get("admin_pass"); ok && pw != "" {
		return cfg.GetDefault("admin_user", "admin"), pw, false, nil
	}
	user := strings.TrimSpace(envUser)
	if user == "" {
		user = "admin"
	}
	pw := strings.TrimSpace(envPass)
	generated := false
	if pw == "" {
		pw = randomPassword(12)
		generated = true
	}
	if err := cfg.Set("admin_user", user); err != nil {
		return "", "", false, err
	}
	if err := cfg.Set("admin_pass", hashPassword(pw)); err != nil {
		return "", "", false, err
	}
	return user, pw, generated, nil
}

// requestID 为请求生成短随机 ID（首次生成后缓存在请求头，便于同一请求的多行日志关联）。
// 只用于日志关联，不构成认证。
func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Lnh-Request-Id"); id != "" {
		return id
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	r.Header.Set("X-Lnh-Request-Id", id)
	return id
}

// loginHandler validates credentials (from the DB) and issues a session cookie.
func (s *sessionStore) loginHandler(w http.ResponseWriter, r *http.Request) {
	rid := requestID(r)
	if !s.guard.allow(r) {
		log.Printf("login rate-limited rid=%s ip=%s", rid, clientKey(r))
		writeErr(w, http.StatusTooManyRequests, "失败次数过多，请 5 分钟后再试")
		return
	}
	var req struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	wantUser, _ := s.cfg.Get("admin_user")
	wantPass, _ := s.cfg.Get("admin_pass")
	okU := subtle.ConstantTimeCompare([]byte(req.User), []byte(wantUser)) == 1
	okP := okU && verifyPassword(wantPass, req.Pass)
	if !(okU && okP) {
		// 失败审计：记录用户名与来源 IP（不记录密码/令牌），用于排查暴力尝试。
		log.Printf("login failed rid=%s user=%q ip=%s", rid, req.User, clientKey(r))
		if d := s.guard.fail(r); d > 0 {
			writeErr(w, http.StatusTooManyRequests, "失败次数过多，请 5 分钟后再试")
			return
		}
		writeErr(w, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	// 旧版明文密码首次验证通过后自动升级为加盐哈希
	if !strings.HasPrefix(wantPass, passPrefix) {
		s.upgradePassword(r, req.Pass)
	} else {
		s.guard.success(r)
	}
	token := randomToken()
	exp := time.Now().Add(sessionTTL)
	s.mu.Lock()
	s.sessions[token] = exp
	s.mu.Unlock()
	// Secure 仅在 HTTPS 环境下置位：应用自身不终止 TLS，由反向代理的
	// X-Forwarded-Proto=https 或直达 TLS 判定；纯 HTTP 内网登录不受影响。
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure, MaxAge: int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "已登录", "user": wantUser})
}

// logoutHandler clears the session.
func (s *sessionStore) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "已退出"})
}

// meHandler reports current auth status (200 authed / 401 not).
func (s *sessionStore) meHandler(w http.ResponseWriter, r *http.Request) {
	if s.valid(r) {
		writeJSON(w, http.StatusOK, map[string]string{"user": s.cfg.GetDefault("admin_user", "admin")})
		return
	}
	writeErr(w, http.StatusUnauthorized, "未登录")
}

// changePasswordHandler (authenticated) validates the old password and updates
// the admin password in the database.
func (s *sessionStore) changePasswordHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPass string `json:"oldPass"`
		NewPass string `json:"newPass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cur, _ := s.cfg.Get("admin_pass")
	if !verifyPassword(cur, req.OldPass) {
		writeErr(w, http.StatusUnauthorized, "旧密码错误")
		return
	}
	if len(req.NewPass) < 6 {
		writeErr(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	if err := s.cfg.Set("admin_pass", hashPassword(req.NewPass)); err != nil {
		writeErr(w, http.StatusInternalServerError, "密码更新失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "密码已修改"})
}

// requireAuth guards an http.Handler; returns 401 when the session is invalid.
func (s *sessionStore) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.valid(r) {
			writeErr(w, http.StatusUnauthorized, "未登录或会话过期")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *sessionStore) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func randomPassword(n int) string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	rb := make([]byte, n)
	_, _ = rand.Read(rb)
	for i := range b {
		b[i] = chars[int(rb[i])%len(chars)]
	}
	return string(b)
}
