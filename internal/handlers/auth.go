package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"stor.app/api/internal/auth"
	"stor.app/api/internal/httpx"
	"stor.app/api/internal/reqctx"
	"stor.app/api/internal/store"
)

type tokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"` // access-token lifetime, seconds
}

type authResponse struct {
	User   userResponse `json:"user"`
	Tokens tokenPair    `json:"tokens"`
}

// dummyHash keeps signin timing uniform when the email doesn't exist: we
// verify the supplied password against this instead of returning early.
var dummyHash, _ = auth.HashPassword("timing-equalizer-dummy-password")

func (h *Handler) Signup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	email, err := normalizeEmail(req.Email)
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if !validName(req.Name) {
		httpx.BadRequest(w, "name must be 1-100 characters")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		h.Log.Error("hash password", "error", err)
		httpx.Internal(w)
		return
	}

	var user store.User
	var pair tokenPair
	err = h.withTx(r.Context(), func(q *store.Queries) error {
		var err error
		user, err = q.CreateUser(r.Context(), store.CreateUserParams{
			Email: email, PasswordHash: hash, Name: req.Name,
		})
		if err != nil {
			return err
		}
		pair, err = h.issueTokens(r.Context(), q, user.ID)
		return err
	})
	if isUniqueViolation(err) {
		httpx.Error(w, http.StatusConflict, "email_taken", "an account with this email already exists")
		return
	}
	if err != nil {
		h.Log.Error("signup", "error", err)
		httpx.Internal(w)
		return
	}

	h.audit(r.Context(), "signup", "user_id", user.ID)
	httpx.JSON(w, http.StatusCreated, authResponse{User: toUserResponse(user), Tokens: pair})
}

func (h *Handler) Signin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	email, err := normalizeEmail(req.Email)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}

	user, err := h.Store.GetUserByEmail(r.Context(), email)
	hash := dummyHash
	if err == nil {
		hash = user.PasswordHash
	}
	match, verr := auth.VerifyPassword(req.Password, hash)
	if verr != nil || err != nil || !match {
		h.audit(r.Context(), "signin_failed", "email_known", err == nil)
		httpx.Error(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}

	pair, err := h.issueTokens(r.Context(), h.Store, user.ID)
	if err != nil {
		h.Log.Error("issue tokens", "error", err)
		httpx.Internal(w)
		return
	}
	h.audit(r.Context(), "signin", "user_id", user.ID)
	httpx.JSON(w, http.StatusOK, authResponse{User: toUserResponse(user), Tokens: pair})
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := httpx.Decode(w, r, &req); err != nil || req.RefreshToken == "" {
		httpx.BadRequest(w, "refreshToken is required")
		return
	}

	row, err := h.Store.GetRefreshTokenByHash(r.Context(), auth.HashToken(req.RefreshToken))
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	// A rotated (revoked) token being presented again means it leaked or was
	// replayed: kill the whole family so the thief's copy dies too.
	if row.RevokedAt != nil {
		if err := h.Store.RevokeRefreshTokenFamily(r.Context(), row.FamilyID); err != nil {
			h.Log.Error("revoke family", "error", err)
		}
		h.audit(r.Context(), "refresh_token_reuse_detected", "user_id", row.UserID, "family_id", row.FamilyID)
		httpx.Unauthorized(w)
		return
	}
	if time.Now().After(row.ExpiresAt) {
		httpx.Unauthorized(w)
		return
	}

	var pair tokenPair
	err = h.withTx(r.Context(), func(q *store.Queries) error {
		token, hash, err := auth.NewOpaqueToken()
		if err != nil {
			return err
		}
		next, err := q.CreateRefreshToken(r.Context(), store.CreateRefreshTokenParams{
			UserID: row.UserID, TokenHash: hash, FamilyID: row.FamilyID,
			ExpiresAt: time.Now().Add(h.Cfg.RefreshTokenTTL),
		})
		if err != nil {
			return err
		}
		if err := q.RotateRefreshToken(r.Context(), store.RotateRefreshTokenParams{
			ID: row.ID, ReplacedBy: &next.ID,
		}); err != nil {
			return err
		}
		access, err := auth.MintAccessToken(h.Cfg.JWTSigningKey, row.UserID, h.Cfg.AccessTokenTTL)
		if err != nil {
			return err
		}
		pair = tokenPair{AccessToken: access, RefreshToken: token, ExpiresIn: int(h.Cfg.AccessTokenTTL.Seconds())}
		return nil
	})
	if err != nil {
		h.Log.Error("refresh", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]tokenPair{"tokens": pair})
}

func (h *Handler) Signout(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.UserID(r.Context())
	if err := h.Store.RevokeAllUserRefreshTokens(r.Context(), userID); err != nil {
		h.Log.Error("signout", "error", err)
		httpx.Internal(w)
		return
	}
	h.audit(r.Context(), "signout", "user_id", userID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	// Deliberately identical response whether or not the account exists.
	defer httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "if the account exists, a reset link has been sent"})

	email, err := normalizeEmail(req.Email)
	if err != nil {
		return
	}
	user, err := h.Store.GetUserByEmail(r.Context(), email)
	if err != nil {
		return
	}
	token, hash, err := auth.NewOpaqueToken()
	if err != nil {
		h.Log.Error("reset token", "error", err)
		return
	}
	if err := h.Store.CreatePasswordResetToken(r.Context(), store.CreatePasswordResetTokenParams{
		TokenHash: hash, UserID: user.ID, ExpiresAt: time.Now().Add(h.Cfg.ResetTokenTTL),
	}); err != nil {
		h.Log.Error("store reset token", "error", err)
		return
	}
	if err := h.Mail.SendPasswordReset(email, token); err != nil {
		h.Log.Error("send reset mail", "error", err)
	}
	h.audit(r.Context(), "password_reset_requested", "user_id", user.ID)
}

func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := httpx.Decode(w, r, &req); err != nil || req.Token == "" {
		httpx.BadRequest(w, "token and newPassword are required")
		return
	}
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}

	row, err := h.Store.GetPasswordResetToken(r.Context(), auth.HashToken(req.Token))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_token", "reset token is invalid or has expired")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		h.Log.Error("hash password", "error", err)
		httpx.Internal(w)
		return
	}
	err = h.withTx(r.Context(), func(q *store.Queries) error {
		if err := q.UpdateUserPassword(r.Context(), store.UpdateUserPasswordParams{ID: row.UserID, PasswordHash: hash}); err != nil {
			return err
		}
		if err := q.MarkPasswordResetTokenUsed(r.Context(), row.TokenHash); err != nil {
			return err
		}
		// A password change ends every existing session.
		return q.RevokeAllUserRefreshTokens(r.Context(), row.UserID)
	})
	if err != nil {
		h.Log.Error("reset password", "error", err)
		httpx.Internal(w)
		return
	}
	h.audit(r.Context(), "password_reset", "user_id", row.UserID)
	w.WriteHeader(http.StatusNoContent)
}

// issueTokens mints an access token and starts a fresh refresh-token family.
func (h *Handler) issueTokens(ctx context.Context, q *store.Queries, userID uuid.UUID) (tokenPair, error) {
	access, err := auth.MintAccessToken(h.Cfg.JWTSigningKey, userID, h.Cfg.AccessTokenTTL)
	if err != nil {
		return tokenPair{}, err
	}
	refresh, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return tokenPair{}, err
	}
	if _, err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{
		UserID: userID, TokenHash: hash, FamilyID: uuid.New(),
		ExpiresAt: time.Now().Add(h.Cfg.RefreshTokenTTL),
	}); err != nil {
		return tokenPair{}, err
	}
	return tokenPair{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(h.Cfg.AccessTokenTTL.Seconds())}, nil
}
