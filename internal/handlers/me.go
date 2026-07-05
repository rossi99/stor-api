package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"stor.app/api/internal/domain"
	"stor.app/api/internal/httpx"
	"stor.app/api/internal/reqctx"
	"stor.app/api/internal/store"
)

type userResponse struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Age       *int32    `json:"age"`
	Gender    *string   `json:"gender"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"createdAt"`
}

func toUserResponse(u store.User) userResponse {
	return userResponse{
		ID: u.ID, Name: u.Name, Email: u.Email, Age: u.Age,
		Gender: u.Gender, Currency: u.Currency, CreatedAt: u.CreatedAt,
	}
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.UserID(r.Context())
	user, err := h.Store.GetUserByID(r.Context(), userID)
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	httpx.JSON(w, http.StatusOK, toUserResponse(user))
}

func (h *Handler) PatchMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.UserID(r.Context())
	var req struct {
		Name     *string `json:"name"`
		Age      *int32  `json:"age"`
		Gender   *string `json:"gender"`
		Currency *string `json:"currency"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.Name != nil && !validName(*req.Name) {
		httpx.BadRequest(w, "name must be 1-100 characters")
		return
	}
	if req.Age != nil && (*req.Age < 0 || *req.Age > 150) {
		httpx.BadRequest(w, "age must be between 0 and 150")
		return
	}
	if req.Gender != nil && len(*req.Gender) > 50 {
		httpx.BadRequest(w, "gender must be at most 50 characters")
		return
	}
	if req.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.Currency))
		if len(c) != 3 {
			httpx.BadRequest(w, "currency must be a 3-letter ISO code")
			return
		}
		req.Currency = &c
	}

	user, err := h.Store.UpdateUserProfile(r.Context(), store.UpdateUserProfileParams{
		ID: userID, Name: req.Name, Age: req.Age, Gender: req.Gender, Currency: req.Currency,
	})
	if err != nil {
		h.Log.Error("update profile", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, toUserResponse(user))
}

type taxInfoBody struct {
	GrossMonthlyMinor          int64   `json:"grossMonthlyMinor"`
	TaxCode                    string  `json:"taxCode"`
	TaxSystem                  string  `json:"taxSystem"`
	TaxYear                    string  `json:"taxYear"`
	MaritalStatus              string  `json:"maritalStatus"`
	PensionContributionPercent int32   `json:"pensionContributionPercent"`
	HasStudentLoan             bool    `json:"hasStudentLoan"`
	StudentLoanPlan            *string `json:"studentLoanPlan"`
}

func (h *Handler) GetTaxInfo(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.UserID(r.Context())
	ti, err := h.Store.GetTaxInfo(r.Context(), userID)
	if isNoRows(err) {
		httpx.NotFound(w)
		return
	}
	if err != nil {
		h.Log.Error("get tax info", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, taxInfoBody{
		GrossMonthlyMinor: ti.GrossMonthlyMinor, TaxCode: ti.TaxCode, TaxSystem: ti.TaxSystem,
		TaxYear: ti.TaxYear, MaritalStatus: ti.MaritalStatus,
		PensionContributionPercent: ti.PensionContributionPercent,
		HasStudentLoan:             ti.HasStudentLoan, StudentLoanPlan: ti.StudentLoanPlan,
	})
}

func (h *Handler) PutTaxInfo(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.UserID(r.Context())
	var req taxInfoBody
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if !domain.ValidAmount(req.GrossMonthlyMinor) {
		httpx.BadRequest(w, "grossMonthlyMinor out of range")
		return
	}
	if req.PensionContributionPercent < 0 || req.PensionContributionPercent > 100 {
		httpx.BadRequest(w, "pensionContributionPercent must be 0-100")
		return
	}
	for _, s := range []string{req.TaxCode, req.TaxSystem, req.TaxYear, req.MaritalStatus} {
		if len(s) > 50 {
			httpx.BadRequest(w, "field too long")
			return
		}
	}

	ti, err := h.Store.UpsertTaxInfo(r.Context(), store.UpsertTaxInfoParams{
		UserID: userID, GrossMonthlyMinor: req.GrossMonthlyMinor, TaxCode: req.TaxCode,
		TaxSystem: req.TaxSystem, TaxYear: req.TaxYear, MaritalStatus: req.MaritalStatus,
		PensionContributionPercent: req.PensionContributionPercent,
		HasStudentLoan:             req.HasStudentLoan, StudentLoanPlan: req.StudentLoanPlan,
	})
	if err != nil {
		h.Log.Error("upsert tax info", "error", err)
		httpx.Internal(w)
		return
	}
	req.GrossMonthlyMinor = ti.GrossMonthlyMinor
	httpx.JSON(w, http.StatusOK, req)
}

type incomeResponse struct {
	UserID            uuid.UUID `json:"userId"`
	MemberName        string    `json:"memberName,omitempty"`
	GrossMonthlyMinor int64     `json:"grossMonthlyMinor"`
	NetMonthlyMinor   int64     `json:"netMonthlyMinor"`
	Currency          string    `json:"currency"`
}

func (h *Handler) GetMyIncome(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	inc, err := h.Store.GetIncomeByUserID(r.Context(), m.UserID)
	if isNoRows(err) {
		httpx.NotFound(w)
		return
	}
	if err != nil {
		h.Log.Error("get income", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, incomeResponse{
		UserID: inc.UserID, GrossMonthlyMinor: inc.GrossMonthlyMinor,
		NetMonthlyMinor: inc.NetMonthlyMinor, Currency: "GBP",
	})
}

func (h *Handler) PutMyIncome(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	var req struct {
		GrossMonthlyMinor int64 `json:"grossMonthlyMinor"`
		NetMonthlyMinor   int64 `json:"netMonthlyMinor"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if !domain.ValidAmount(req.GrossMonthlyMinor) || !domain.ValidAmount(req.NetMonthlyMinor) {
		httpx.BadRequest(w, "amounts out of range")
		return
	}
	if req.NetMonthlyMinor > req.GrossMonthlyMinor {
		httpx.BadRequest(w, "net income cannot exceed gross income")
		return
	}

	inc, err := h.Store.UpsertIncome(r.Context(), store.UpsertIncomeParams{
		HouseholdID: m.HouseholdID, UserID: m.UserID,
		GrossMonthlyMinor: req.GrossMonthlyMinor, NetMonthlyMinor: req.NetMonthlyMinor,
	})
	if err != nil {
		h.Log.Error("upsert income", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, incomeResponse{
		UserID: inc.UserID, GrossMonthlyMinor: inc.GrossMonthlyMinor,
		NetMonthlyMinor: inc.NetMonthlyMinor, Currency: "GBP",
	})
}

func (h *Handler) PutMyPension(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	var req struct {
		CurrentValueMinor int64 `json:"currentValueMinor"`
		TargetMinor       int64 `json:"targetMinor"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if !domain.ValidAmount(req.CurrentValueMinor) || req.TargetMinor <= 0 || req.TargetMinor > domain.MaxAmountMinor {
		httpx.BadRequest(w, "amounts out of range (target must be positive)")
		return
	}

	p, err := h.Store.UpsertPension(r.Context(), store.UpsertPensionParams{
		HouseholdID: m.HouseholdID, UserID: m.UserID,
		CurrentValueMinor: req.CurrentValueMinor, TargetMinor: req.TargetMinor,
	})
	if err != nil {
		h.Log.Error("upsert pension", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"userId": p.UserID, "currentValueMinor": p.CurrentValueMinor,
		"targetMinor": p.TargetMinor, "lastUpdated": p.UpdatedAt, "currency": "GBP",
	})
}

func (h *Handler) PutMyISA(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	var req struct {
		Type                       string `json:"type"`
		BalanceMinor               int64  `json:"balanceMinor"`
		ContributionsThisYearMinor int64  `json:"contributionsThisYearMinor"`
		AnnualAllowanceMinor       int64  `json:"annualAllowanceMinor"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if len(req.Type) == 0 || len(req.Type) > 100 {
		httpx.BadRequest(w, "type must be 1-100 characters")
		return
	}
	for _, v := range []int64{req.BalanceMinor, req.ContributionsThisYearMinor, req.AnnualAllowanceMinor} {
		if !domain.ValidAmount(v) {
			httpx.BadRequest(w, "amounts out of range")
			return
		}
	}
	if req.ContributionsThisYearMinor > req.AnnualAllowanceMinor {
		httpx.BadRequest(w, "contributions cannot exceed the annual allowance")
		return
	}

	isa, err := h.Store.UpsertISA(r.Context(), store.UpsertISAParams{
		HouseholdID: m.HouseholdID, UserID: m.UserID, Type: req.Type,
		BalanceMinor:               req.BalanceMinor,
		ContributionsThisYearMinor: req.ContributionsThisYearMinor,
		AnnualAllowanceMinor:       req.AnnualAllowanceMinor,
	})
	if err != nil {
		h.Log.Error("upsert isa", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"userId": isa.UserID, "type": isa.Type, "balanceMinor": isa.BalanceMinor,
		"contributionsThisYearMinor": isa.ContributionsThisYearMinor,
		"annualAllowanceMinor":       isa.AnnualAllowanceMinor, "currency": "GBP",
	})
}
