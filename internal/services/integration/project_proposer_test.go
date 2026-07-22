package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalProjectProposer_Name(t *testing.T) {
	t.Parallel()
	pp := NewInternalProjectProposer("tok", "http://localhost")
	if pp.Name() != "project" {
		t.Errorf("Name() = %q, want %q", pp.Name(), "project")
	}
}

func TestInternalProjectProposer_ProposeProject_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/internal/projects/propose" {
			t.Errorf("path = %q, want /api/v1/internal/projects/propose", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}

		var params ProposeProjectParams
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if params.Title != "Test Project" {
			t.Errorf("title = %q", params.Title)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ProposeProjectResult{ID: "proj-123"})
	}))
	defer srv.Close()

	pp := NewInternalProjectProposer("test-token", srv.URL)
	result, err := pp.ProposeProject(context.Background(), ProposeProjectParams{
		RepositoryID: "repo-1",
		Title:        "Test Project",
		Goal:         "Test goal",
		Reasoning:    "Test reasoning",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "proj-123" {
		t.Errorf("id = %q, want %q", result.ID, "proj-123")
	}
}

func TestInternalProjectProposer_ProposeProject_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	pp := NewInternalProjectProposer("token", srv.URL)
	_, err := pp.ProposeProject(context.Background(), ProposeProjectParams{
		RepositoryID: "repo-1",
		Title:        "Test",
		Goal:         "Goal",
		Reasoning:    "Reason",
	})

	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestInternalProjectProposer_ProposeProject_BadURL(t *testing.T) {
	t.Parallel()

	pp := NewInternalProjectProposer("token", "http://127.0.0.1:0")
	_, err := pp.ProposeProject(context.Background(), ProposeProjectParams{
		RepositoryID: "repo-1",
		Title:        "Test",
		Goal:         "Goal",
		Reasoning:    "Reason",
	})

	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}
