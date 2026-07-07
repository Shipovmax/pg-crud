package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"pg-crud/logging"
	"pg-crud/repository"
)

var emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// UserHandler handles HTTP requests for the users resource.
type UserHandler struct {
	repo repository.UserRepository
}

// NewUserHandler constructs a UserHandler backed by the given repository.
func NewUserHandler(repo repository.UserRepository) *UserHandler {
	return &UserHandler{repo: repo}
}

type createUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// updateUserRequest carries the optimistic-lock version the client read;
// a stale version is rejected with 409 instead of silently overwriting.
type updateUserRequest struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Version int64  `json:"version"`
}

func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !emailRE.MatchString(req.Email) {
		writeError(w, http.StatusBadRequest, "email is invalid")
		return
	}

	u, err := h.repo.Create(r.Context(), req.Name, req.Email)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateEmail) {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		logging.FromContext(r.Context()).Error("create user", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	writeJSON(w, http.StatusCreated, u)
}

func (h *UserHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	u, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		logging.FromContext(r.Context()).Error("get user", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	writeJSON(w, http.StatusOK, u)
}

const (
	defaultListLimit = 20
	maxListLimit     = 100
)

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > maxListLimit {
			writeError(w, http.StatusBadRequest, "limit must not exceed 100")
			return
		}
		limit = n
	}

	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}

	users, err := h.repo.List(r.Context(), limit, offset)
	if err != nil {
		logging.FromContext(r.Context()).Error("list users", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	writeJSON(w, http.StatusOK, users)
}

func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !emailRE.MatchString(req.Email) {
		writeError(w, http.StatusBadRequest, "email is invalid")
		return
	}
	if req.Version < 1 {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}

	u, err := h.repo.Update(r.Context(), id, req.Name, req.Email, req.Version)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if errors.Is(err, repository.ErrDuplicateEmail) {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		if errors.Is(err, repository.ErrVersionConflict) {
			writeError(w, http.StatusConflict, "version conflict")
			return
		}
		logging.FromContext(r.Context()).Error("update user", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	writeJSON(w, http.StatusOK, u)
}

func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := h.repo.Delete(r.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		logging.FromContext(r.Context()).Error("delete user", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Error("write json response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
