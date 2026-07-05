package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"stor.app/api/internal/domain"
	"stor.app/api/internal/httpx"
	"stor.app/api/internal/reqctx"
	"stor.app/api/internal/store"
)

type expenseResponse struct {
	ID           uuid.UUID   `json:"id"`
	Name         string      `json:"name"`
	AmountMinor  int64       `json:"amountMinor"`
	MonthlyMinor int64       `json:"monthlyMinor"`
	Currency     string      `json:"currency"`
	Frequency    string      `json:"frequency"`
	Category     string      `json:"category"`
	Bucket       string      `json:"bucket"`
	AddedBy      uuid.UUID   `json:"addedBy"`
	Status       string      `json:"status"`
	AgreedBy     []uuid.UUID `json:"agreedBy"`
	CreatedAt    time.Time   `json:"createdAt"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}

func toExpenseResponse(e store.Expense, agreedBy []uuid.UUID) expenseResponse {
	if agreedBy == nil {
		agreedBy = []uuid.UUID{}
	}
	cat := domain.Category(e.Category)
	return expenseResponse{
		ID: e.ID, Name: e.Name, AmountMinor: e.AmountMinor,
		MonthlyMinor: domain.MonthlyMinor(e.AmountMinor, domain.Frequency(e.Frequency)),
		Currency:     "GBP", Frequency: e.Frequency, Category: e.Category,
		Bucket: string(cat.Bucket()), AddedBy: e.AddedBy, Status: e.Status,
		AgreedBy: agreedBy, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

func (h *Handler) ListExpenses(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())

	var status *string
	if s := r.URL.Query().Get("status"); s != "" {
		if s != domain.ExpenseStatusPending && s != domain.ExpenseStatusApproved {
			httpx.BadRequest(w, "status must be 'pending' or 'approved'")
			return
		}
		status = &s
	}

	expenses, err := h.Store.ListExpenses(r.Context(), store.ListExpensesParams{
		HouseholdID: m.HouseholdID, Status: status,
	})
	if err != nil {
		h.Log.Error("list expenses", "error", err)
		httpx.Internal(w)
		return
	}
	approvals, err := h.Store.ListExpenseApprovalsByHousehold(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("list approvals", "error", err)
		httpx.Internal(w)
		return
	}
	agreed := make(map[uuid.UUID][]uuid.UUID, len(approvals))
	for _, a := range approvals {
		agreed[a.ExpenseID] = append(agreed[a.ExpenseID], a.UserID)
	}

	out := make([]expenseResponse, 0, len(expenses))
	for _, e := range expenses {
		out = append(out, toExpenseResponse(e, agreed[e.ID]))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"expenses": out})
}

type expenseBody struct {
	Name        string `json:"name"`
	AmountMinor int64  `json:"amountMinor"`
	Frequency   string `json:"frequency"`
	Category    string `json:"category"`
}

func (b expenseBody) validate() string {
	if !validName(b.Name) {
		return "name must be 1-100 characters"
	}
	if !domain.ValidAmount(b.AmountMinor) {
		return "amountMinor out of range"
	}
	if !domain.Frequency(b.Frequency).Valid() {
		return "frequency must be weekly, monthly or annual"
	}
	if !domain.Category(b.Category).Valid() {
		return "unknown category"
	}
	return ""
}

func (h *Handler) CreateExpense(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	var req expenseBody
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if msg := req.validate(); msg != "" {
		httpx.BadRequest(w, msg)
		return
	}

	memberCount, err := h.Store.CountMembers(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("count members", "error", err)
		httpx.Internal(w)
		return
	}

	// Same rule as AddExpenseView.addExpense(): approvals only kick in when
	// enabled and there is someone else to agree.
	status := domain.ExpenseStatusApproved
	if m.RequiresApprovals && memberCount > 1 {
		status = domain.ExpenseStatusPending
	}

	var expense store.Expense
	var agreedBy []uuid.UUID
	err = h.withTx(r.Context(), func(q *store.Queries) error {
		var err error
		expense, err = q.CreateExpense(r.Context(), store.CreateExpenseParams{
			HouseholdID: m.HouseholdID, Name: req.Name, AmountMinor: req.AmountMinor,
			Frequency: req.Frequency, Category: req.Category, AddedBy: m.UserID, Status: status,
		})
		if err != nil {
			return err
		}
		if status == domain.ExpenseStatusPending {
			// The adder implicitly agrees to their own expense.
			if _, err := q.UpsertExpenseApproval(r.Context(), store.UpsertExpenseApprovalParams{
				ExpenseID: expense.ID, UserID: m.UserID,
			}); err != nil {
				return err
			}
			agreedBy = []uuid.UUID{m.UserID}
		}
		return nil
	})
	if err != nil {
		h.Log.Error("create expense", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusCreated, toExpenseResponse(expense, agreedBy))
}

func (h *Handler) UpdateExpense(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	expenseID, err := pathUUID(r, "expenseID")
	if err != nil {
		httpx.BadRequest(w, "invalid expense id")
		return
	}
	var req struct {
		Name        *string `json:"name"`
		AmountMinor *int64  `json:"amountMinor"`
		Frequency   *string `json:"frequency"`
		Category    *string `json:"category"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.Name != nil && !validName(*req.Name) {
		httpx.BadRequest(w, "name must be 1-100 characters")
		return
	}
	if req.AmountMinor != nil && !domain.ValidAmount(*req.AmountMinor) {
		httpx.BadRequest(w, "amountMinor out of range")
		return
	}
	if req.Frequency != nil && !domain.Frequency(*req.Frequency).Valid() {
		httpx.BadRequest(w, "frequency must be weekly, monthly or annual")
		return
	}
	if req.Category != nil && !domain.Category(*req.Category).Valid() {
		httpx.BadRequest(w, "unknown category")
		return
	}

	existing, err := h.Store.GetExpense(r.Context(), store.GetExpenseParams{ID: expenseID, HouseholdID: m.HouseholdID})
	if isNoRows(err) {
		httpx.NotFound(w)
		return
	}
	if err != nil {
		h.Log.Error("get expense", "error", err)
		httpx.Internal(w)
		return
	}
	if existing.AddedBy != m.UserID && !m.IsCreator {
		httpx.Forbidden(w)
		return
	}

	expense, err := h.Store.UpdateExpense(r.Context(), store.UpdateExpenseParams{
		ID: expenseID, HouseholdID: m.HouseholdID,
		Name: req.Name, AmountMinor: req.AmountMinor, Frequency: req.Frequency, Category: req.Category,
	})
	if err != nil {
		h.Log.Error("update expense", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, toExpenseResponse(expense, h.agreedBy(r, expense.ID)))
}

func (h *Handler) DeleteExpense(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	expenseID, err := pathUUID(r, "expenseID")
	if err != nil {
		httpx.BadRequest(w, "invalid expense id")
		return
	}
	existing, err := h.Store.GetExpense(r.Context(), store.GetExpenseParams{ID: expenseID, HouseholdID: m.HouseholdID})
	if isNoRows(err) {
		httpx.NotFound(w)
		return
	}
	if err != nil {
		h.Log.Error("get expense", "error", err)
		httpx.Internal(w)
		return
	}
	if existing.AddedBy != m.UserID && !m.IsCreator {
		httpx.Forbidden(w)
		return
	}
	if _, err := h.Store.SoftDeleteExpense(r.Context(), store.SoftDeleteExpenseParams{
		ID: expenseID, HouseholdID: m.HouseholdID,
	}); err != nil {
		h.Log.Error("delete expense", "error", err)
		httpx.Internal(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ApproveExpense records the caller's sign-off and flips the expense to
// approved once every current member has agreed — all inside one transaction
// so concurrent approvals can't double-count.
func (h *Handler) ApproveExpense(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	expenseID, err := pathUUID(r, "expenseID")
	if err != nil {
		httpx.BadRequest(w, "invalid expense id")
		return
	}

	var expense store.Expense
	var agreedBy []uuid.UUID
	txErr := h.withTx(r.Context(), func(q *store.Queries) error {
		var err error
		expense, err = q.GetExpense(r.Context(), store.GetExpenseParams{ID: expenseID, HouseholdID: m.HouseholdID})
		if err != nil {
			return err
		}
		if expense.Status != domain.ExpenseStatusPending {
			return errNotPending
		}
		if _, err := q.UpsertExpenseApproval(r.Context(), store.UpsertExpenseApprovalParams{
			ExpenseID: expenseID, UserID: m.UserID,
		}); err != nil {
			return err
		}
		approvals, err := q.CountExpenseApprovals(r.Context(), expenseID)
		if err != nil {
			return err
		}
		members, err := q.CountMembers(r.Context(), m.HouseholdID)
		if err != nil {
			return err
		}
		if approvals >= members {
			if err := q.SetExpenseApproved(r.Context(), expenseID); err != nil {
				return err
			}
			expense.Status = domain.ExpenseStatusApproved
		}
		return nil
	})
	if isNoRows(txErr) {
		httpx.NotFound(w)
		return
	}
	if txErr == errNotPending {
		httpx.Error(w, http.StatusConflict, "not_pending", "this expense is not awaiting approval")
		return
	}
	if txErr != nil {
		h.Log.Error("approve expense", "error", txErr)
		httpx.Internal(w)
		return
	}
	h.audit(r.Context(), "expense_approved", "expense_id", expenseID, "user_id", m.UserID, "status", expense.Status)
	agreedBy = h.agreedBy(r, expenseID)
	httpx.JSON(w, http.StatusOK, toExpenseResponse(expense, agreedBy))
}

var errNotPending = &notPendingError{}

type notPendingError struct{}

func (*notPendingError) Error() string { return "expense is not pending" }

// agreedBy fetches the approver list for a single expense; failures degrade to
// an empty list rather than failing the request.
func (h *Handler) agreedBy(r *http.Request, expenseID uuid.UUID) []uuid.UUID {
	m, _ := reqctx.MembershipFrom(r.Context())
	approvals, err := h.Store.ListExpenseApprovalsByHousehold(r.Context(), m.HouseholdID)
	if err != nil {
		return nil
	}
	var out []uuid.UUID
	for _, a := range approvals {
		if a.ExpenseID == expenseID {
			out = append(out, a.UserID)
		}
	}
	return out
}
