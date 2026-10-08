package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

const authCookieName = "foreman_session"

type tokenAuth struct {
	hash [sha256.Size]byte
}

func newTokenAuth(token string) *tokenAuth {
	if token == "" {
		return nil
	}
	return &tokenAuth{hash: sha256.Sum256([]byte(token))}
}

func (a *tokenAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/v1/auth" || r.URL.Path == "/api/v1/auth/session" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !a.validRequest(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cyber-foreman"`)
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", errors.New("API authentication is required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *tokenAuth) validRequest(r *http.Request) bool {
	if a == nil {
		return true
	}
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") && a.matches(fields[1]) {
		return true
	}
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return false
	}
	expected := hex.EncodeToString(a.hash[:])
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expected)) == 1
}

func (a *tokenAuth) matches(candidate string) bool {
	hash := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(a.hash[:], hash[:]) == 1
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	required := s.auth != nil
	writeJSON(w, http.StatusOK, map[string]bool{
		"required": required, "authenticated": !required || s.auth.validRequest(r),
	})
}

func (s *Server) createAuthSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.auth == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	if !s.auth.matches(request.Token) {
		writeAPIError(w, http.StatusUnauthorized, "invalid_token", errors.New("API token is invalid"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: authCookieName, Value: hex.EncodeToString(s.auth.hash[:]), Path: "/",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) deleteAuthSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{
		Name: authCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}
