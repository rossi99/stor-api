package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"stor.app/api/internal/auth"
	"stor.app/api/internal/domain"
	"stor.app/api/internal/httpx"
	"stor.app/api/internal/reqctx"
	"stor.app/api/internal/store"
)

type frameworkBody struct {
	NeedsPercent   int32 `json:"needsPercent"`
	WantsPercent   int32 `json:"wantsPercent"`
	SavingsPercent int32 `json:"savingsPercent"`
}

func (f frameworkBody) valid() bool {
	for _, p := range []int32{f.NeedsPercent, f.WantsPercent, f.SavingsPercent} {
		if p < 0 || p > 100 {
			return false
		}
	}
	return f.NeedsPercent+f.WantsPercent+f.SavingsPercent == 100
}

type memberResponse struct {
	UserID    uuid.UUID `json:"userId"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Age       *int32    `json:"age"`
	Gender    *string   `json:"gender"`
	IsCreator bool      `json:"isCreator"`
	JoinedAt  time.Time `json:"joinedAt"`
}

type householdResponse struct {
	ID                 uuid.UUID        `json:"id"`
	Name               string           `json:"name"`
	Framework          frameworkBody    `json:"framework"`
	RequiresApprovals  bool             `json:"requiresApprovals"`
	SurplusSplitMethod string           `json:"surplusSplitMethod"`
	InviteCode         string           `json:"inviteCode"`
	CreatedAt          time.Time        `json:"createdAt"`
	Members            []memberResponse `json:"members,omitempty"`
}

func toHouseholdResponse(hh store.Household, members []store.ListMembersRow) householdResponse {
	resp := householdResponse{
		ID:   hh.ID,
		Name: hh.Name,
		Framework: frameworkBody{
			NeedsPercent: hh.NeedsPercent, WantsPercent: hh.WantsPercent, SavingsPercent: hh.SavingsPercent,
		},
		RequiresApprovals:  hh.RequiresApprovals,
		SurplusSplitMethod: hh.SurplusSplitMethod,
		InviteCode:         hh.InviteCode,
		CreatedAt:          hh.CreatedAt,
	}
	for _, m := range members {
		resp.Members = append(resp.Members, memberResponse{
			UserID: m.UserID, Name: m.Name, Email: m.Email, Age: m.Age,
			Gender: m.Gender, IsCreator: m.IsCreator, JoinedAt: m.JoinedAt,
		})
	}
	return resp
}

func (h *Handler) CreateHousehold(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.UserID(r.Context())
	var req struct {
		Name              string        `json:"name"`
		Framework         frameworkBody `json:"framework"`
		RequiresApprovals bool          `json:"requiresApprovals"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if !validName(req.Name) {
		httpx.BadRequest(w, "name must be 1-100 characters")
		return
	}
	if !req.Framework.valid() {
		httpx.BadRequest(w, "framework percentages must be 0-100 and sum to 100")
		return
	}
	if _, err := h.Store.GetMembershipByUserID(r.Context(), userID); err == nil {
		httpx.Error(w, http.StatusConflict, "already_in_household", "you already belong to a household")
		return
	}

	var hh store.Household
	err := h.withTx(r.Context(), func(q *store.Queries) error {
		// Retry on the (astronomically unlikely) invite-code collision.
		for attempt := 0; ; attempt++ {
			code, err := auth.NewInviteCode()
			if err != nil {
				return err
			}
			hh, err = q.CreateHousehold(r.Context(), store.CreateHouseholdParams{
				Name:              req.Name,
				NeedsPercent:      req.Framework.NeedsPercent,
				WantsPercent:      req.Framework.WantsPercent,
				SavingsPercent:    req.Framework.SavingsPercent,
				RequiresApprovals: req.RequiresApprovals,
				InviteCode:        code,
			})
			if err == nil {
				break
			}
			if !isUniqueViolation(err) || attempt >= 3 {
				return err
			}
		}
		_, err := q.CreateMember(r.Context(), store.CreateMemberParams{
			HouseholdID: hh.ID, UserID: userID, IsCreator: true,
		})
		return err
	})
	if isUniqueViolation(err) {
		httpx.Error(w, http.StatusConflict, "already_in_household", "you already belong to a household")
		return
	}
	if err != nil {
		h.Log.Error("create household", "error", err)
		httpx.Internal(w)
		return
	}
	h.audit(r.Context(), "household_created", "household_id", hh.ID, "user_id", userID)
	httpx.JSON(w, http.StatusCreated, toHouseholdResponse(hh, nil))
}

func (h *Handler) JoinHousehold(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.UserID(r.Context())
	var req struct {
		InviteCode string `json:"inviteCode"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	code := normalizeInviteCode(req.InviteCode)
	if len(code) != 9 { // XXXX-XXXX
		httpx.BadRequest(w, "invite code must be 8 characters")
		return
	}

	hh, err := h.Store.GetHouseholdByInviteCode(r.Context(), code)
	if err != nil {
		h.audit(r.Context(), "join_failed", "user_id", userID)
		httpx.Error(w, http.StatusNotFound, "invalid_code", "invite code not recognised")
		return
	}
	_, err = h.Store.CreateMember(r.Context(), store.CreateMemberParams{
		HouseholdID: hh.ID, UserID: userID, IsCreator: false,
	})
	if isUniqueViolation(err) {
		httpx.Error(w, http.StatusConflict, "already_in_household", "you already belong to a household")
		return
	}
	if err != nil {
		h.Log.Error("join household", "error", err)
		httpx.Internal(w)
		return
	}
	h.audit(r.Context(), "household_joined", "household_id", hh.ID, "user_id", userID)

	members, _ := h.Store.ListMembers(r.Context(), hh.ID)
	httpx.JSON(w, http.StatusOK, toHouseholdResponse(hh, members))
}

// normalizeInviteCode uppercases and re-inserts the display hyphen so users
// can type the code with or without it.
func normalizeInviteCode(code string) string {
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}

func (h *Handler) GetHousehold(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	hh, err := h.Store.GetHouseholdByID(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("get household", "error", err)
		httpx.Internal(w)
		return
	}
	members, err := h.Store.ListMembers(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("list members", "error", err)
		httpx.Internal(w)
		return
	}
	httpx.JSON(w, http.StatusOK, toHouseholdResponse(hh, members))
}

func (h *Handler) PatchHousehold(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	if !m.IsCreator {
		httpx.Forbidden(w)
		return
	}
	var req struct {
		Name               *string        `json:"name"`
		Framework          *frameworkBody `json:"framework"`
		RequiresApprovals  *bool          `json:"requiresApprovals"`
		SurplusSplitMethod *string        `json:"surplusSplitMethod"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.Name != nil && !validName(*req.Name) {
		httpx.BadRequest(w, "name must be 1-100 characters")
		return
	}
	if req.Framework != nil && !req.Framework.valid() {
		httpx.BadRequest(w, "framework percentages must be 0-100 and sum to 100")
		return
	}
	if req.SurplusSplitMethod != nil && !domain.SplitMethod(*req.SurplusSplitMethod).Valid() {
		httpx.BadRequest(w, "surplusSplitMethod must be 'proportional' or 'even'")
		return
	}

	params := store.UpdateHouseholdParams{
		ID: m.HouseholdID, Name: req.Name,
		RequiresApprovals: req.RequiresApprovals, SurplusSplitMethod: req.SurplusSplitMethod,
	}
	if req.Framework != nil {
		params.NeedsPercent = &req.Framework.NeedsPercent
		params.WantsPercent = &req.Framework.WantsPercent
		params.SavingsPercent = &req.Framework.SavingsPercent
	}
	hh, err := h.Store.UpdateHousehold(r.Context(), params)
	if err != nil {
		h.Log.Error("update household", "error", err)
		httpx.Internal(w)
		return
	}
	members, _ := h.Store.ListMembers(r.Context(), m.HouseholdID)
	httpx.JSON(w, http.StatusOK, toHouseholdResponse(hh, members))
}

func (h *Handler) RotateInviteCode(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	if !m.IsCreator {
		httpx.Forbidden(w)
		return
	}
	var hh store.Household
	var err error
	for attempt := 0; ; attempt++ {
		var code string
		code, err = auth.NewInviteCode()
		if err != nil {
			break
		}
		hh, err = h.Store.RotateInviteCode(r.Context(), store.RotateInviteCodeParams{
			ID: m.HouseholdID, InviteCode: code,
		})
		if err == nil || !isUniqueViolation(err) || attempt >= 3 {
			break
		}
	}
	if err != nil {
		h.Log.Error("rotate invite code", "error", err)
		httpx.Internal(w)
		return
	}
	h.audit(r.Context(), "invite_code_rotated", "household_id", m.HouseholdID, "user_id", m.UserID)
	httpx.JSON(w, http.StatusOK, map[string]string{"inviteCode": hh.InviteCode})
}

func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	members, err := h.Store.ListMembers(r.Context(), m.HouseholdID)
	if err != nil {
		h.Log.Error("list members", "error", err)
		httpx.Internal(w)
		return
	}
	out := make([]memberResponse, 0, len(members))
	for _, mem := range members {
		out = append(out, memberResponse{
			UserID: mem.UserID, Name: mem.Name, Email: mem.Email, Age: mem.Age,
			Gender: mem.Gender, IsCreator: mem.IsCreator, JoinedAt: mem.JoinedAt,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"members": out})
}

func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	m, _ := reqctx.MembershipFrom(r.Context())
	if !m.IsCreator {
		httpx.Forbidden(w)
		return
	}
	target, err := pathUUID(r, "userID")
	if err != nil {
		httpx.BadRequest(w, "invalid member id")
		return
	}
	if target == m.UserID {
		httpx.BadRequest(w, "the creator cannot remove themselves")
		return
	}
	// The SQL additionally guards is_creator = false, so a creator row can
	// never be removed even by a bug above this line.
	rows, err := h.Store.SoftDeleteMember(r.Context(), store.SoftDeleteMemberParams{
		HouseholdID: m.HouseholdID, UserID: target,
	})
	if err != nil {
		h.Log.Error("remove member", "error", err)
		httpx.Internal(w)
		return
	}
	if rows == 0 {
		httpx.NotFound(w)
		return
	}
	h.audit(r.Context(), "member_removed", "household_id", m.HouseholdID, "removed_user_id", target, "by", m.UserID)
	w.WriteHeader(http.StatusNoContent)
}
