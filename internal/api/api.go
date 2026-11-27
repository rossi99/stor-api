package api

import (
	"cmp"
	"context"
	"crypto/ecdsa"
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
	"unicode/utf8"

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
	// attempts limits password and invite-code guesses per client address; joins, per account.
	attempts, joins *limiter
	// providers verifies Apple and Google ID tokens, keyed by provider; one not configured is absent.
	providers map[string]*verifier
	apple     *appleRevoker
}

// Config turns on Apple and Google sign-in; Apple deletions also revoke the grant when AppleKey is set.
type Config struct {
	GoogleClientID                         string
	AppleBundleID, AppleTeamID, AppleKeyID string
	AppleKey                               *ecdsa.PrivateKey
	// Tests point these at a fake provider.
	googleKeysURL, appleURL string
}

func New(pool *pgxpool.Pool, cfg Config) http.Handler {
	s := &Server{pool: pool, q: store.New(pool), attempts: newLimiter(20, 3*time.Second), joins: newLimiter(5, 10*time.Minute),
		providers: map[string]*verifier{}}
	client := &http.Client{Timeout: 10 * time.Second}
	if cfg.GoogleClientID != "" {
		s.providers["google"] = &verifier{keysURL: cmp.Or(cfg.googleKeysURL, "https://www.googleapis.com/oauth2/v3/certs"),
			issuers: []string{"accounts.google.com", "https://accounts.google.com"}, audience: cfg.GoogleClientID, client: client}
	}
	if cfg.AppleBundleID != "" {
		base := cmp.Or(cfg.appleURL, appleURL)
		s.providers["apple"] = &verifier{keysURL: base + "/auth/keys", issuers: []string{appleURL}, audience: cfg.AppleBundleID, client: client}
		if cfg.AppleKey != nil {
			s.apple = &appleRevoker{baseURL: base, clientID: cfg.AppleBundleID, teamID: cfg.AppleTeamID, keyID: cfg.AppleKeyID, key: cfg.AppleKey, client: client}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /auth/sign-up", s.limited(s.signUp))
	mux.HandleFunc("POST /auth/sign-in", s.limited(s.signIn))
	mux.HandleFunc("POST /auth/provider", s.limited(s.providerSignIn))
	mux.HandleFunc("DELETE /auth/account", s.limited(s.authed(s.deleteAccount)))
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
	mux.HandleFunc("PUT /household/members", s.authed(s.setMembers))
	mux.HandleFunc("POST /household/invites", s.authed(s.createInvite))
	mux.HandleFunc("POST /household/join", s.limited(s.authed(s.joinHousehold)))
	mux.HandleFunc("PUT /household/budget", s.authed(s.setBudget))
	mux.HandleFunc("PUT /bills/{id}", s.authed(s.putBill))
	mux.HandleFunc("DELETE /bills/{id}", s.authed(s.deleteBill))
	mux.HandleFunc("GET /plan", s.authed(s.getPlan))
	mux.HandleFunc("PUT /plan/transfers", s.authed(s.setTransfer))
	return mux
}

const maxMembers = 4

// viewer is the signed-in member; slot is their absolute ledger slot (1 to size).
type viewer struct {
	household pgtype.UUID
	user      pgtype.UUID
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
	// Method is "password", "apple" or "google": how the account signs in and re-confirms deletion.
	Method string `json:"method"`
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
	token, err := s.createAccount(r.Context(), c.Email, pgtype.Text{String: hash, Valid: true}, nil)
	if emailTaken(err) {
		fail(w, http.StatusConflict, "email taken")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusCreated, authSession{Email: c.Email, Token: token, Method: "password"})
}

// createAccount makes a user in a new two-seat household and signs them in; Apple and Google users have no hash.
func (s *Server) createAccount(ctx context.Context, email string, hash pgtype.Text, id *store.CreateIdentityParams) (string, error) {
	var token string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		userID, err := q.CreateUser(ctx, store.CreateUserParams{Email: email, PasswordHash: hash})
		if err != nil {
			return err
		}
		if id != nil {
			id.UserID = userID
			if err := q.CreateIdentity(ctx, *id); err != nil {
				return err
			}
		}
		household, err := q.CreateHousehold(ctx)
		if err != nil {
			return err
		}
		if err := q.CreateMember(ctx, store.CreateMemberParams{HouseholdID: household, Slot: 1, UserID: userID}); err != nil {
			return err
		}
		if err := q.CreateMember(ctx, store.CreateMemberParams{HouseholdID: household, Slot: 2}); err != nil {
			return err
		}
		token, err = newSession(ctx, q, userID)
		return err
	})
	return token, err
}

func emailTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key"
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	c.normalise()
	user, err := s.q.GetUserByEmail(r.Context(), c.Email)
	// Apple and Google accounts have a NULL hash, which never verifies.
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !verifyPassword(c.Password, user.PasswordHash.String)) {
		fail(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	s.openSession(w, r, user.ID, authSession{Email: c.Email, Method: "password"})
}

// providerSignIn signs in with an Apple or Google ID token, creating the account on first use.
// An email that already has an account gets 409: sign-up never verifies emails, so linking could hand someone else's account over.
func (s *Server) providerSignIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		IDToken  string `json:"idToken"`
	}
	if !decode(w, r, &body) {
		return
	}
	id, ok := s.verifyIdentity(w, r, body.Provider, body.IDToken)
	if !ok {
		return
	}
	user, err := s.q.GetUserByIdentity(r.Context(), store.GetUserByIdentityParams{Provider: body.Provider, Subject: id.subject})
	if err == nil {
		s.openSession(w, r, user.ID, authSession{Email: user.Email, Method: body.Provider})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		internal(w, r, err)
		return
	}
	if id.email == "" || !id.emailVerified {
		fail(w, http.StatusBadRequest, "verified email required")
		return
	}
	token, err := s.createAccount(r.Context(), id.email, pgtype.Text{}, &store.CreateIdentityParams{Provider: body.Provider, Subject: id.subject})
	if emailTaken(err) {
		fail(w, http.StatusConflict, "email registered")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusCreated, authSession{Email: id.email, Token: token, Method: body.Provider})
}

// verifyIdentity writes the error response itself when the token can't be trusted.
func (s *Server) verifyIdentity(w http.ResponseWriter, r *http.Request, provider, token string) (identity, bool) {
	v, ok := s.providers[provider]
	if !ok {
		fail(w, http.StatusBadRequest, "unsupported provider")
		return identity{}, false
	}
	id, err := v.verify(r.Context(), token)
	if errors.Is(err, errInvalidToken) {
		fail(w, http.StatusUnauthorized, "invalid credentials")
		return identity{}, false
	}
	if err != nil {
		internal(w, r, err)
		return identity{}, false
	}
	return id, true
}

// openSession replies with a new session for an existing user.
func (s *Server) openSession(w http.ResponseWriter, r *http.Request, userID pgtype.UUID, session authSession) {
	member, err := s.q.GetMemberByUser(r.Context(), userID)
	if err != nil {
		internal(w, r, err)
		return
	}
	session.HasCompletedSetup = member.SetupComplete
	session.Token, err = newSession(r.Context(), s.q, userID)
	if err != nil {
		internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, session)
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
	reply(w, http.StatusOK, authSession{Email: u.Email, HasCompletedSetup: u.SetupComplete, Method: u.Method})
}

// deleteAccount removes the signed-in user after re-checking their password, or a fresh Apple or Google sign-in. Alone in the household,
// the whole household goes; otherwise their personal bills, spending, name and pay go and the seat reopens.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, v viewer) {
	var body struct {
		Password          string `json:"password"`
		IDToken           string `json:"idToken"`
		AuthorizationCode string `json:"authorizationCode"`
	}
	if !decode(w, r, &body) {
		return
	}
	creds, err := s.q.GetCredentials(r.Context(), v.user)
	if err != nil {
		internal(w, r, err)
		return
	}
	if creds.PasswordHash.Valid && !verifyPassword(body.Password, creds.PasswordHash.String) {
		fail(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !creds.PasswordHash.Valid {
		id, ok := s.verifyIdentity(w, r, creds.Provider.String, body.IDToken)
		if !ok {
			return
		}
		if id.subject != creds.Subject.String || time.Since(id.issuedAt) > 5*time.Minute {
			fail(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		ctx := r.Context()
		if err := q.LockHousehold(ctx, v.household); err != nil {
			return err
		}
		others, err := q.OtherAccountsInHousehold(ctx, store.OtherAccountsInHouseholdParams{HouseholdID: v.household, Slot: v.slot})
		if err != nil {
			return err
		}
		if !others {
			if err := q.DeleteHousehold(ctx, v.household); err != nil {
				return err
			}
			return q.DeleteUser(ctx, v.user)
		}
		if err := q.DeletePersonalBills(ctx, store.DeletePersonalBillsParams{HouseholdID: v.household, Slot: v.slot}); err != nil {
			return err
		}
		if err := q.DeletePersonalTransactions(ctx, store.DeletePersonalTransactionsParams{HouseholdID: v.household, Slot: v.slot}); err != nil {
			return err
		}
		if err := q.ForgetActorName(ctx, store.ForgetActorNameParams{HouseholdID: v.household, Slot: v.slot}); err != nil {
			return err
		}
		if err := q.ReleaseSeat(ctx, store.ReleaseSeatParams{HouseholdID: v.household, Slot: v.slot}); err != nil {
			return err
		}
		return q.DeleteUser(ctx, v.user)
	})
	if err != nil {
		internal(w, r, err)
		return
	}
	// The account is already gone, so a failed revoke is only logged; the user can still remove Stór under their Apple ID settings.
	if creds.Provider.String == "apple" && s.apple != nil && body.AuthorizationCode != "" {
		if err := s.apple.revoke(context.WithoutCancel(r.Context()), body.AuthorizationCode); err != nil {
			slog.Error("apple revoke failed", "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
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
		next(w, r, viewer{household: m.HouseholdID, user: m.UserID, slot: m.Slot, size: m.Size})
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
	HasAccount   bool    `json:"hasAccount"`
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

// shortcut: nothing sets roles yet, so they go out as ""; add a column when an endpoint writes them.
// averageSharedOutgoings, emergencyFund and netWorth are omitted until there is data behind them.
type snapshot struct {
	Name             string          `json:"name"`
	Members          []member        `json:"members"`
	TopUpDay         int32           `json:"topUpDay"`
	Ledgers          []ledgerSummary `json:"ledgers"`
	Transactions     []transaction   `json:"transactions"`
	Goals            []goal          `json:"goals"`
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
		Members:  []member{}, Transactions: []transaction{}, Goals: []goal{},
		AccountGroups: []any{}, Recaps: []any{}, Payslips: []any{},
		Alerts: []any{}, LinkableAccounts: []any{}, LinkedAccountIDs: []string{},
	}

	members, err := s.q.ListMembers(ctx, v.household)
	if err != nil {
		internal(w, r, err)
		return
	}
	// The viewer first, then everyone else in slot order.
	for _, m := range members {
		out := member{
			Ledger: v.ledgerFor(m.Slot), Name: m.Name, Initials: initials(m.Name), NetPay: pounds(m.NetPayPence),
			Contribution: pounds(m.ContributionPence), HasAccount: m.HasAccount,
		}
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

// Any member may name anyone and set their pay, as in a shared budget sheet; pay is visible to the household anyway.
func (s *Server) setMembers(w http.ResponseWriter, r *http.Request, v viewer) {
	var body []struct {
		Ledger string  `json:"ledger"`
		Name   string  `json:"name"`
		NetPay float64 `json:"netPay"`
	}
	if !decode(w, r, &body) {
		return
	}
	updates := make([]store.SetMemberProfileParams, 0, len(body))
	for _, m := range body {
		slot, ok := v.slotFor(m.Ledger)
		pay, payOK := pence(m.NetPay)
		name := strings.TrimSpace(m.Name)
		if !ok || slot == 0 || !payOK || pay < 0 || utf8.RuneCountInString(name) > 60 {
			fail(w, http.StatusBadRequest, "invalid member")
			return
		}
		updates = append(updates, store.SetMemberProfileParams{HouseholdID: v.household, Slot: slot, Name: name, NetPayPence: pay})
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		for _, u := range updates {
			if err := s.q.WithTx(tx).SetMemberProfile(r.Context(), u); err != nil {
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

// initials takes the first letter of the first and last names: "Ana María Ruiz" is "AR".
func initials(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return ""
	}
	first := string([]rune(words[0])[:1])
	if len(words) == 1 {
		return strings.ToUpper(first)
	}
	return strings.ToUpper(first + string([]rune(words[len(words)-1])[:1]))
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
