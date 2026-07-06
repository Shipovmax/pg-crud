package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"pg-crud/repository"
)

type fakeUserRepository struct {
	users  map[int64]*repository.User
	nextID int64
}

func newFakeUserRepository() *fakeUserRepository {
	return &fakeUserRepository{users: make(map[int64]*repository.User)}
}

func (f *fakeUserRepository) Create(_ context.Context, name, email string) (*repository.User, error) {
	for _, u := range f.users {
		if u.Email == email {
			return nil, repository.ErrDuplicateEmail
		}
	}
	f.nextID++
	u := &repository.User{ID: f.nextID, Name: name, Email: email, CreatedAt: time.Now()}
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeUserRepository) GetByID(_ context.Context, id int64) (*repository.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (f *fakeUserRepository) List(_ context.Context, limit, offset int) ([]*repository.User, error) {
	ids := make([]int64, 0, len(f.users))
	for id := range f.users {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	users := make([]*repository.User, 0, limit)
	for i := offset; i < len(ids) && len(users) < limit; i++ {
		users = append(users, f.users[ids[i]])
	}
	return users, nil
}

func (f *fakeUserRepository) Update(_ context.Context, id int64, name, email string) (*repository.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	for otherID, other := range f.users {
		if otherID != id && other.Email == email {
			return nil, repository.ErrDuplicateEmail
		}
	}
	u.Name = name
	u.Email = email
	return u, nil
}

func (f *fakeUserRepository) Delete(_ context.Context, id int64) error {
	if _, ok := f.users[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.users, id)
	return nil
}

func newTestHandler() (*UserHandler, *fakeUserRepository) {
	repo := newFakeUserRepository()
	return NewUserHandler(repo), repo
}

func doRequest(h http.HandlerFunc, method, target string, body any, idParam string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	if idParam != "" {
		req.SetPathValue("id", idParam)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestCreate(t *testing.T) {
	h, _ := newTestHandler()

	tests := []struct {
		name       string
		body       any
		wantStatus int
	}{
		{"valid", createUserRequest{Name: "Alice", Email: "alice@example.com"}, http.StatusCreated},
		{"missing name", createUserRequest{Email: "bob@example.com"}, http.StatusBadRequest},
		{"invalid email", createUserRequest{Name: "Bob", Email: "not-an-email"}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(h.Create, http.MethodPost, "/users", tt.body, "")
			if rec.Code != tt.wantStatus {
				t.Fatalf("got status %d, want %d, body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestCreateDuplicateEmail(t *testing.T) {
	h, _ := newTestHandler()
	doRequest(h.Create, http.MethodPost, "/users", createUserRequest{Name: "Alice", Email: "alice@example.com"}, "")

	rec := doRequest(h.Create, http.MethodPost, "/users", createUserRequest{Name: "Alice2", Email: "alice@example.com"}, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestGetByID(t *testing.T) {
	h, repo := newTestHandler()
	u, _ := repo.Create(context.Background(), "Alice", "alice@example.com")

	rec := doRequest(h.GetByID, http.MethodGet, "/users/"+itoa(u.ID), nil, itoa(u.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}

	rec = doRequest(h.GetByID, http.MethodGet, "/users/999", nil, "999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusNotFound)
	}

	rec = doRequest(h.GetByID, http.MethodGet, "/users/abc", nil, "abc")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdate(t *testing.T) {
	h, repo := newTestHandler()
	u, _ := repo.Create(context.Background(), "Alice", "alice@example.com")

	rec := doRequest(h.Update, http.MethodPut, "/users/"+itoa(u.ID), updateUserRequest{Name: "Alice2", Email: "alice2@example.com"}, itoa(u.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec = doRequest(h.Update, http.MethodPut, "/users/999", updateUserRequest{Name: "X", Email: "x@example.com"}, "999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDelete(t *testing.T) {
	h, repo := newTestHandler()
	u, _ := repo.Create(context.Background(), "Alice", "alice@example.com")

	rec := doRequest(h.Delete, http.MethodDelete, "/users/"+itoa(u.ID), nil, itoa(u.ID))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusNoContent)
	}

	rec = doRequest(h.Delete, http.MethodDelete, "/users/"+itoa(u.ID), nil, itoa(u.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestList(t *testing.T) {
	h, repo := newTestHandler()
	for i := range 3 {
		repo.Create(context.Background(), "User"+itoa(int64(i)), "user"+itoa(int64(i))+"@example.com")
	}

	rec := doRequest(h.List, http.MethodGet, "/users?limit=2&offset=1", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got []*repository.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d users, want 2", len(got))
	}

	rec = doRequest(h.List, http.MethodGet, "/users?limit=101", nil, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = doRequest(h.List, http.MethodGet, "/users?limit=abc", nil, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
