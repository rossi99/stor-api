package api

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"rossi99/stor/internal/store"
)

const inviteTTL = 48 * time.Hour

// No 0/O, 1/I or L, so a code read aloud or retyped can't be mistaken.
const inviteAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newInviteCode() string {
	code := make([]byte, 8)
	for i := range code {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(inviteAlphabet))))
		code[i] = inviteAlphabet[n.Int64()]
	}
	return string(code[:4]) + "-" + string(code[4:])
}

// inviteHash ignores case, spaces and dashes, so "abcd efgh" matches "ABCD-EFGH".
func inviteHash(code string) []byte {
	normalised := strings.Map(func(r rune) rune {
		if r = unicode.ToUpper(r); strings.ContainsRune(inviteAlphabet, r) {
			return r
		}
		return -1
	}, code)
	hash := sha256.Sum256([]byte(normalised))
	return hash[:]
}

// A new invite replaces the household's previous one.
func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, v viewer) {
	open, err := s.q.HasOpenSeat(r.Context(), v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	if !open {
		fail(w, http.StatusConflict, "household is full")
		return
	}
	code := newInviteCode()
	expires := time.Now().Add(inviteTTL).UTC().Truncate(time.Second)
	err = s.q.UpsertInvite(r.Context(), store.UpsertInviteParams{
		HouseholdID: v.household, CodeHash: inviteHash(code), ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusCreated, map[string]any{"code": code, "expiresAt": expires})
}

// joinHousehold moves the viewer into the invite's household, taking its first empty seat
// and discarding the household sign-up made for them, which must still be empty.
func (s *Server) joinHousehold(w http.ResponseWriter, r *http.Request, v viewer) {
	var body struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	errNoInvite, errSame, errFull, errHasData := errors.New("no invite"), errors.New("same"), errors.New("full"), errors.New("has data")
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		target, err := q.TakeInvite(r.Context(), inviteHash(body.Code))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoInvite
		}
		if err != nil {
			return err
		}
		if target == v.household {
			return errSame
		}
		if err := q.LockHousehold(r.Context(), target); err != nil {
			return err
		}
		slot, err := q.OpenSeat(r.Context(), target)
		if errors.Is(err, pgx.ErrNoRows) {
			return errFull
		}
		if err != nil {
			return err
		}
		if err := q.LockHousehold(r.Context(), v.household); err != nil {
			return err
		}
		disposable, err := q.HouseholdIsDisposable(r.Context(), store.HouseholdIsDisposableParams{HouseholdID: v.household, Slot: v.slot})
		if err != nil {
			return err
		}
		if !disposable.Bool {
			return errHasData
		}
		if err := q.DeleteHousehold(r.Context(), v.household); err != nil {
			return err
		}
		return q.ClaimSeat(r.Context(), store.ClaimSeatParams{HouseholdID: target, Slot: slot, UserID: v.user})
	})
	switch {
	case errors.Is(err, errNoInvite):
		fail(w, http.StatusNotFound, "invalid or expired code")
		return
	case errors.Is(err, errSame):
		fail(w, http.StatusConflict, "already in this household")
		return
	case errors.Is(err, errFull):
		fail(w, http.StatusConflict, "household is full")
		return
	case errors.Is(err, errHasData):
		fail(w, http.StatusConflict, "your household already has data")
		return
	case err != nil:
		internal(w, r, err)
		return
	}
	hash, _ := bearerHash(r)
	u, err := s.q.GetSessionUser(r.Context(), hash)
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, authSession{Email: u.Email, HasCompletedSetup: u.SetupComplete})
}
