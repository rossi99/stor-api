package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"rossi99/stor/internal/store"
)

const sessionTTL = 30 * 24 * time.Hour

type Server struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

func New(pool *pgxpool.Pool) http.Handler {
	s := &Server{pool: pool, q: store.New(pool)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /auth/sign-up", s.signUp)
	mux.HandleFunc("POST /auth/sign-in", s.signIn)
	mux.HandleFunc("POST /auth/sign-out", s.signOut)
	mux.HandleFunc("GET /auth/session", s.session)
	mux.HandleFunc("GET /household", s.authed(s.household))
	mux.HandleFunc("POST /transactions", s.authed(s.addTransaction))
	mux.HandleFunc("PUT /household/contributions", s.authed(s.setContributions))
	mux.HandleFunc("PUT /goals/{id}/monthly", s.authed(s.setGoalMonthly))
	mux.HandleFunc("PUT /household/linked-accounts", s.authed(s.setLinkedAccounts))
	mux.HandleFunc("PUT /household/top-up-day", s.authed(s.setTopUpDay))
	mux.HandleFunc("PUT /household/size", s.authed(s.setHouseholdSize))
	mux.HandleFunc("POST /household/setup-complete", s.authed(s.completeSetup))
	return mux
}

const maxMembers = 4

// viewer is the signed-in member; slot is their absolute ledger slot (1 to size).
type viewer struct {
	household pgtype.UUID
	slot      int16
	size      int16
}

// The other members, in slot order, as the viewer sees them.
var otherLedgers = [maxMembers - 1]string{"partner", "third", "fourth"}

// Ledgers on the wire are relative to the viewer; slot 0 is always the joint pot.
func (v viewer) slotFor(ledger string) (int16, bool) {
	switch ledger {
	case "you":
		return v.slot, true
	case "joint":
		return 0, true
	}
	for i, name := range otherLedgers {
		if name == ledger {
			slot := int16(i) + 1
			if slot >= v.slot {
				slot++
			}
			return slot, slot <= v.size
		}
	}
	return 0, false
}

func (v viewer) ledgerFor(slot int16) string {
	switch {
	case slot == 0:
		return "joint"
	case slot == v.slot:
		return "you"
	case slot < v.slot:
		return otherLedgers[slot-1]
	}
	return otherLedgers[slot-2]
}

// --- auth ---

type credentials struct{ Email, Password string }

type authSession struct {
	Email             string `json:"email"`
	HasCompletedSetup bool   `json:"hasCompletedSetup"`
	Token             string `json:"token,omitempty"`
}

func (c *credentials) normalise() bool {
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	return len(c.Email) <= 254 && strings.Count(c.Email, "@") == 1 &&
		!strings.HasPrefix(c.Email, "@") && !strings.HasSuffix(c.Email, "@")
}

func (s *Server) signUp(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	if !c.normalise() || len(c.Password) < 8 || len(c.Password) > 256 {
		fail(w, http.StatusBadRequest, "invalid email or password")
		return
	}
	hash, err := hashPassword(c.Password)
	if err != nil {
		internal(w, r, err)
		return
	}
	var token string
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		userID, err := q.CreateUser(r.Context(), store.CreateUserParams{Email: c.Email, PasswordHash: hash})
		if err != nil {
			return err
		}
		household, err := q.CreateHousehold(r.Context())
		if err != nil {
			return err
		}
		if err := q.CreateMember(r.Context(), store.CreateMemberParams{HouseholdID: household, Slot: 1, UserID: userID}); err != nil {
			return err
		}
		if err := q.CreateMember(r.Context(), store.CreateMemberParams{HouseholdID: household, Slot: 2}); err != nil {
			return err
		}
		token, err = newSession(r.Context(), q, userID)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key" {
		fail(w, http.StatusConflict, "email taken")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusCreated, authSession{Email: c.Email, Token: token})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	c.normalise()
	user, err := s.q.GetUserByEmail(r.Context(), c.Email)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !verifyPassword(c.Password, user.PasswordHash)) {
		fail(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	member, err := s.q.GetMemberByUser(r.Context(), user.ID)
	if err != nil {
		internal(w, r, err)
		return
	}
	token, err := newSession(r.Context(), s.q, user.ID)
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, authSession{Email: c.Email, HasCompletedSetup: member.SetupComplete, Token: token})
}

func newSession(ctx context.Context, q *store.Queries, userID pgtype.UUID) (string, error) {
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, q.CreateSession(ctx, store.CreateSessionParams{
		TokenHash: hash[:],
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(sessionTTL), Valid: true},
	})
}

// Revoking is idempotent: an unknown or expired token is already signed out.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	hash, ok := bearerHash(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "missing token")
		return
	}
	if err := s.q.DeleteSession(r.Context(), hash); err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// session lets a relaunched app check its stored token without signing in again.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	hash, ok := bearerHash(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "missing token")
		return
	}
	u, err := s.q.GetSessionUser(r.Context(), hash)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, authSession{Email: u.Email, HasCompletedSetup: u.SetupComplete})
}

func bearerHash(r *http.Request) ([]byte, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return nil, false
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:], true
}

func (s *Server) authed(next func(http.ResponseWriter, *http.Request, viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash, ok := bearerHash(r)
		if !ok {
			fail(w, http.StatusUnauthorized, "missing token")
			return
		}
		m, err := s.q.GetSessionMember(r.Context(), hash)
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if err != nil {
			internal(w, r, err)
			return
		}
		next(w, r, viewer{household: m.HouseholdID, slot: m.Slot, size: m.Size})
	}
}

// --- household ---

type member struct {
	Ledger       string  `json:"ledger"`
	Name         string  `json:"name"`
	Initials     string  `json:"initials"`
	Role         string  `json:"role"`
	NetPay       float64 `json:"netPay"`
	Contribution float64 `json:"contribution"`
}

type ledgerSummary struct {
	Ledger     string  `json:"ledger"`
	Available  float64 `json:"available"`
	Spent      float64 `json:"spent"`
	Committed  float64 `json:"committed"`
	Unbudgeted float64 `json:"unbudgeted"`
	Envelopes  []any   `json:"envelopes"`
}

type transaction struct {
	ID           string    `json:"id"`
	Date         time.Time `json:"date"`
	Title        string    `json:"title"`
	Category     string    `json:"category"`
	Amount       float64   `json:"amount"`
	Actor        *string   `json:"actor"`
	ActorName    *string   `json:"actorName"`
	Ledger       string    `json:"ledger"`
	Tag          string    `json:"tag"`
	SplitPercent *int      `json:"splitPercent"`
	Monogram     string    `json:"monogram"`
}

type goal struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Saved   float64 `json:"saved"`
	Target  float64 `json:"target"`
	Monthly float64 `json:"monthly"`
	Note    string  `json:"note"`
}

// shortcut: nothing sets names, roles or net pay yet, so they go out as "" and 0; add columns when an endpoint writes them.
// averageSharedOutgoings, emergencyFund and netWorth are omitted until there is data behind them.
type snapshot struct {
	Name             string          `json:"name"`
	Members          []member        `json:"members"`
	TopUpDay         int32           `json:"topUpDay"`
	Ledgers          []ledgerSummary `json:"ledgers"`
	Transactions     []transaction   `json:"transactions"`
	Goals            []goal          `json:"goals"`
	Bills            []any           `json:"bills"`
	AccountGroups    []any           `json:"accountGroups"`
	Recaps           []any           `json:"recaps"`
	Payslips         []any           `json:"payslips"`
	Alerts           []any           `json:"alerts"`
	LinkableAccounts []any           `json:"linkableAccounts"`
	LinkedAccountIDs []string        `json:"linkedAccountIDs"`
}

func (s *Server) household(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	topUpDay, err := s.q.GetTopUpDay(ctx, v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	snap := snapshot{
		TopUpDay: topUpDay,
		Members: []member{}, Transactions: []transaction{}, Goals: []goal{},
		Bills: []any{}, AccountGroups: []any{}, Recaps: []any{}, Payslips: []any{},
		Alerts: []any{}, LinkableAccounts: []any{}, LinkedAccountIDs: []string{},
	}

	members, err := s.q.ListMembers(ctx, v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	// The viewer first, then everyone else in slot order.
	for _, m := range members {
		out := member{Ledger: v.ledgerFor(m.Slot), Contribution: pounds(m.ContributionPence)}
		if m.Slot == v.slot {
			snap.Members = append([]member{out}, snap.Members...)
		} else {
			snap.Members = append(snap.Members, out)
		}
	}

	now := time.Now().UTC()
	spent, err := s.q.SpentByLedger(ctx, store.SpentByLedgerParams{
		HouseholdID: v.household,
		Since:       pgtype.Timestamptz{Time: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), Valid: true},
	})
	if err != nil {
		internal(w, r, err)
		return
	}
	spentBySlot := map[int16]int64{}
	for _, row := range spent {
		spentBySlot[row.LedgerSlot] = row.SpentPence
	}
	slots := []int16{v.slot, 0}
	for slot := int16(1); slot <= v.size; slot++ {
		if slot != v.slot {
			slots = append(slots, slot)
		}
	}
	for _, slot := range slots {
		snap.Ledgers = append(snap.Ledgers, ledgerSummary{Ledger: v.ledgerFor(slot), Spent: pounds(spentBySlot[slot]), Envelopes: []any{}})
	}

	txns, err := s.q.ListVisibleTransactions(ctx, store.ListVisibleTransactionsParams{HouseholdID: v.household, ViewerSlot: v.slot})
	if err != nil {
		internal(w, r, err)
		return
	}
	for _, t := range txns {
		out := transaction{
			ID: t.ID, Date: t.OccurredAt.Time.UTC().Truncate(time.Second), Title: t.Title, Category: t.Category,
			Amount: pounds(t.AmountPence), Ledger: v.ledgerFor(t.LedgerSlot), Tag: t.Tag, Monogram: t.Monogram,
		}
		if t.ActorSlot.Valid {
			actor := v.ledgerFor(t.ActorSlot.Int16)
			out.Actor = &actor
		}
		if t.ActorName.Valid {
			out.ActorName = &t.ActorName.String
		}
		if t.SplitPercent.Valid {
			split := int(t.SplitPercent.Int32)
			out.SplitPercent = &split
		}
		snap.Transactions = append(snap.Transactions, out)
	}

	goals, err := s.q.ListGoals(ctx, v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	for _, g := range goals {
		snap.Goals = append(snap.Goals, goal{
			ID: g.ID, Name: g.Name, Saved: pounds(g.SavedPence), Target: pounds(g.TargetPence),
			Monthly: pounds(g.MonthlyPence), Note: g.Note,
		})
	}
	reply(w, http.StatusOK, snap)
}

// --- writes ---

func (s *Server) addTransaction(w http.ResponseWriter, r *http.Request, v viewer) {
	var t transaction
	if !decode(w, r, &t) {
		return
	}
	ledger, ledgerOK := v.slotFor(t.Ledger)
	amount, amountOK := pence(t.Amount)
	if t.ID == "" || len(t.ID) > 64 || t.Date.IsZero() || t.Title == "" || t.Category == "" || t.Monogram == "" ||
		!ledgerOK || !amountOK || amount == 0 || (t.SplitPercent != nil && (*t.SplitPercent < 0 || *t.SplitPercent > 100)) {
		fail(w, http.StatusBadRequest, "invalid transaction")
		return
	}
	params := store.InsertTransactionParams{
		HouseholdID: v.household, ID: t.ID, OccurredAt: pgtype.Timestamptz{Time: t.Date, Valid: true},
		Title: t.Title, Category: t.Category, AmountPence: amount, LedgerSlot: ledger, Tag: t.Tag, Monogram: t.Monogram,
	}
	if t.Actor != nil {
		slot, ok := v.slotFor(*t.Actor)
		if !ok {
			fail(w, http.StatusBadRequest, "invalid actor")
			return
		}
		params.ActorSlot = pgtype.Int2{Int16: slot, Valid: true}
	}
	if t.ActorName != nil {
		params.ActorName = pgtype.Text{String: *t.ActorName, Valid: true}
	}
	if t.SplitPercent != nil {
		params.SplitPercent = pgtype.Int4{Int32: int32(*t.SplitPercent), Valid: true}
	}
	// A retried POST with the same id is a no-op, not a duplicate row.
	if err := s.q.InsertTransaction(r.Context(), params); err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setContributions(w http.ResponseWriter, r *http.Request, v viewer) {
	var body []struct {
		Ledger string  `json:"ledger"`
		Amount float64 `json:"amount"`
	}
	if !decode(w, r, &body) {
		return
	}
	updates := make([]store.SetContributionParams, 0, len(body))
	for _, c := range body {
		slot, ok := v.slotFor(c.Ledger)
		amount, amountOK := pence(c.Amount)
		if !ok || slot == 0 || !amountOK || amount < 0 {
			fail(w, http.StatusBadRequest, "invalid contribution")
			return
		}
		updates = append(updates, store.SetContributionParams{HouseholdID: v.household, Slot: slot, ContributionPence: amount})
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		for _, u := range updates {
			if err := s.q.WithTx(tx).SetContribution(r.Context(), u); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setGoalMonthly(w http.ResponseWriter, r *http.Request, v viewer) {
	var body struct{ Monthly float64 }
	if !decode(w, r, &body) {
		return
	}
	monthly, ok := pence(body.Monthly)
	if !ok || monthly < 0 {
		fail(w, http.StatusBadRequest, "invalid monthly")
		return
	}
	n, err := s.q.SetGoalMonthly(r.Context(), store.SetGoalMonthlyParams{HouseholdID: v.household, ID: r.PathValue("id"), MonthlyPence: monthly})
	if err != nil {
		internal(w, r, err)
		return
	}
	if n == 0 {
		fail(w, http.StatusNotFound, "no such goal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// shortcut: no bank feed yet, so linkableAccounts is empty and only an empty selection is valid; store ids once accounts exist.
func (s *Server) setLinkedAccounts(w http.ResponseWriter, r *http.Request, v viewer) {
	var body struct{ IDs []string }
	if !decode(w, r, &body) {
		return
	}
	if len(body.IDs) > 0 {
		fail(w, http.StatusBadRequest, "unknown account")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setTopUpDay(w http.ResponseWriter, r *http.Request, v viewer) {
	var body struct{ Day int32 }
	if !decode(w, r, &body) {
		return
	}
	if body.Day < 1 || body.Day > 28 {
		fail(w, http.StatusBadRequest, "day must be 1 to 28")
		return
	}
	if err := s.q.SetTopUpDay(r.Context(), store.SetTopUpDayParams{ID: v.household, TopUpDay: body.Day}); err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Growing adds unnamed members; shrinking drops the highest slots, refusing any still in use.
func (s *Server) setHouseholdSize(w http.ResponseWriter, r *http.Request, v viewer) {
	var body struct{ Size int16 }
	if !decode(w, r, &body) {
		return
	}
	if body.Size < 1 || body.Size > maxMembers {
		fail(w, http.StatusBadRequest, "size must be 1 to 4")
		return
	}
	errInUse := errors.New("in use")
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.LockHousehold(r.Context(), v.household); err != nil {
			return err
		}
		inUse, err := q.MembersBeyondInUse(r.Context(), store.MembersBeyondInUseParams{HouseholdID: v.household, Size: body.Size})
		if err != nil {
			return err
		}
		if inUse.Bool {
			return errInUse
		}
		if err := q.DeleteMembersBeyond(r.Context(), store.DeleteMembersBeyondParams{HouseholdID: v.household, Size: body.Size}); err != nil {
			return err
		}
		return q.AddMembersUpTo(r.Context(), store.AddMembersUpToParams{HouseholdID: v.household, Size: int32(body.Size)})
	})
	if errors.Is(err, errInUse) {
		fail(w, http.StatusConflict, "a member being removed has an account or spending")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) completeSetup(w http.ResponseWriter, r *http.Request, v viewer) {
	if err := s.q.CompleteSetup(r.Context(), v.household); err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func pounds(p int64) float64 { return float64(p) / 100 }

// pence rejects NaN, infinities and amounts beyond £10bn.
func pence(f float64) (int64, bool) {
	if math.IsNaN(f) || math.Abs(f) >= 1e10 {
		return 0, false
	}
	return int64(math.Round(f * 100)), true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}

func internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	fail(w, http.StatusInternalServerError, "internal error")
}
