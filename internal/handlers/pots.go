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

type potResponse struct {
	ID                       uuid.UUID `json:"id"`
	Name                     string    `json:"name"`
	CurrentMinor             int64     `json:"currentMinor"`
	TargetMinor              int64     `json:"targetMinor"`
	MonthlyContributionMinor int64     `json:"monthlyContributionMinor"`
	Emoji                    string    `json:"emoji"`
	Currency                 string    `json:"currency"`
	CreatedAt                time.Time `json:"createdAt"`
}

func toPotResponse(p store.SavingsPot) potResponse {
	return potResponse{
		ID: p.ID, Name: p.Name, CurrentMinor: p.CurrentMinor, TargetMinor: p.TargetMinor,
		MonthlyContributionMinor: p.MonthlyContributionMinor, Emoji: p.Emoji,
		Currency: "GBP", CreatedAt: p.CreatedAt,
	}
}

func (h *Handler) ListPots(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	pots, err := h.Store.ListSavingsPots(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("list pots", "error", err)
		httpx.Internal(w)
		return
	}
	out := make([]potResponse, 0, len(pots))
	for _, p := range pots {
		out = append(out, toPotResponse(p))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"pots": out})
}

type potBody struct {
	Name                     string `json:"name"`
	CurrentMinor             int64  `json:"currentMinor"`
	TargetMinor              int64  `json:"targetMinor"`
	MonthlyContributionMinor int64  `json:"monthlyContributionMinor"`
	Emoji                    string `json:"emoji"`
}

func (b potBody) validate() string {
	if !validName(b.Name) {
		return "name must be 1-100 characters"
	}
	if !domain.ValidAmount(b.CurrentMinor) || !domain.ValidAmount(b.MonthlyContributionMinor) {
		return "amounts out of range"
	}
	if b.TargetMinor <= 0 || b.TargetMinor > domain.MaxAmountMinor {
		return "targetMinor must be positive"
	}
	if len(b.Emoji) > 16 {
		return "emoji too long"
	}
	return ""
}

func (h *Handler) CreatePot(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	var req potBody
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if msg := req.validate(); msg != "" {
		httpx.BadRequest(w, msg)
		return
	}
	pot, err := h.Store.CreateSavingsPot(r.Context(), store.CreateSavingsPotParams{
		HouseholdID: m.HouseholdID, Name: req.Name, CurrentMinor: req.CurrentMinor,
		TargetMinor: req.TargetMinor, MonthlyContributionMinor: req.MonthlyContributionMinor,
		Emoji: req.Emoji,
	})
	if err != nil {
		h.Log.Error("create pot", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusCreated, toPotResponse(pot))
}

func (h *Handler) UpdatePot(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	potID, err := pathUUID(r, "potID")
	if err != nil {
		httpx.BadRequest(w, "invalid pot id")
		return
	}
	var req struct {
		Name                     *string `json:"name"`
		CurrentMinor             *int64  `json:"currentMinor"`
		TargetMinor              *int64  `json:"targetMinor"`
		MonthlyContributionMinor *int64  `json:"monthlyContributionMinor"`
		Emoji                    *string `json:"emoji"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.Name != nil && !validName(*req.Name) {
		httpx.BadRequest(w, "name must be 1-100 characters")
		return
	}
	if (req.CurrentMinor != nil && !domain.ValidAmount(*req.CurrentMinor)) ||
		(req.MonthlyContributionMinor != nil && !domain.ValidAmount(*req.MonthlyContributionMinor)) {
		httpx.BadRequest(w, "amounts out of range")
		return
	}
	if req.TargetMinor != nil && (*req.TargetMinor <= 0 || *req.TargetMinor > domain.MaxAmountMinor) {
		httpx.BadRequest(w, "targetMinor must be positive")
		return
	}
	if req.Emoji != nil && len(*req.Emoji) > 16 {
		httpx.BadRequest(w, "emoji too long")
		return
	}

	pot, err := h.Store.UpdateSavingsPot(r.Context(), store.UpdateSavingsPotParams{
		ID: potID, HouseholdID: m.HouseholdID,
		Name: req.Name, CurrentMinor: req.CurrentMinor, TargetMinor: req.TargetMinor,
		MonthlyContributionMinor: req.MonthlyContributionMinor, Emoji: req.Emoji,
	})
	if isNoRows(err) {
		httpx.NotFound(w)
		return
	}
	if err != nil {
		h.Log.Error("update pot", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, toPotResponse(pot))
}

func (h *Handler) DeletePot(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	potID, err := pathUUID(r, "potID")
	if err != nil {
		httpx.BadRequest(w, "invalid pot id")
		return
	}
	rows, err := h.Store.SoftDeleteSavingsPot(r.Context(), store.SoftDeleteSavingsPotParams{
		ID: potID, HouseholdID: m.HouseholdID,
	})
	if err != nil {
		h.Log.Error("delete pot", "error", err)
		httpx.Internal(w)
		return
	}
	if rows == 0 {
		httpx.NotFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
