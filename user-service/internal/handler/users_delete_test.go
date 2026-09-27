package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"user-service/internal/repository"
	"user-service/internal/service"
)

type deleteRepositoryStub struct {
	profileRepositoryStub
	deleteErr  error
	deletedIDs []string
}

func (s *deleteRepositoryStub) SoftDelete(_ context.Context, id string) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

func TestDeleteAccountRejectsInvalidBodies(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"missing confirmation", `{}`, "bad_request", 400},
		{"null confirmation", `{"delete_confirmation":null}`, "bad_request", 400},
		{"false confirmation", `{"delete_confirmation":false}`, "bad_request", 400},
		{"null body", `null`, "bad_request", 400},
		{"empty body", "", "invalid_request", 400},
		{"malformed", `{"delete_confirmation":`, "invalid_request", 400},
		{"array", `[]`, "invalid_request", 400},
		{"string confirmation", `{"delete_confirmation":"true"}`, "invalid_request", 400},
		{"numeric confirmation", `{"delete_confirmation":1}`, "invalid_request", 400},
		{"untrusted account ID", `{"delete_confirmation":true,"id":"another-account"}`, "invalid_request", 400},
		{"second object", `{"delete_confirmation":true}{}`, "invalid_request", 400},
		{"second scalar", `{"delete_confirmation":true}true`, "invalid_request", 400},
		{"trailing garbage", `{"delete_confirmation":true}garbage`, "invalid_request", 400},
		{"oversized body", strings.Repeat(" ", 4096) + `{"delete_confirmation":true}`, "body_too_large", 413},
		{"oversized trailing whitespace", `{"delete_confirmation":true}` + strings.Repeat(" ", 4096), "body_too_large", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &deleteRepositoryStub{profileRepositoryStub: profileRepositoryStub{user: repository.User{Active: true}}}
			res := httptest.NewRecorder()
			deleteAccount(res, accountRequest(http.MethodDelete, tc.body, true), &provisionerStub{accountID: "local-id"}, service.NewUserService(repo, nil))
			assertDeleteResponse(t, res, tc.status, tc.code)
			if repo.reads != 0 || len(repo.deletedIDs) != 0 {
				t.Fatalf("invalid request reached repository: reads=%d deleted=%v", repo.reads, repo.deletedIDs)
			}
		})
	}
}

func TestDeleteAccountConfirmed(t *testing.T) {
	for _, body := range []string{`{"delete_confirmation":true}`, "{\"delete_confirmation\":true} \n\t"} {
		t.Run(body, func(t *testing.T) {
			repo := &deleteRepositoryStub{profileRepositoryStub: profileRepositoryStub{user: repository.User{ID: "local-id", Active: true}}}
			pro := &provisionerStub{accountID: "local-id"}
			res := httptest.NewRecorder()
			deleteAccount(res, accountRequest(http.MethodDelete, body, true), pro, service.NewUserService(repo, nil))
			assertDeleteResponse(t, res, http.StatusOK, "")
			if strings.TrimSpace(res.Body.String()) != "null" {
				t.Fatalf("unexpected success body: %s", res.Body.String())
			}
			if pro.authorizeCalls != 1 || pro.subject != "auth0|student" {
				t.Fatalf("authorization calls=%d subject=%q", pro.authorizeCalls, pro.subject)
			}
			if repo.reads != 1 || len(repo.ids) != 1 || repo.ids[0] != "local-id" || len(repo.deletedIDs) != 1 || repo.deletedIDs[0] != "local-id" {
				t.Fatalf("expected authenticated account only: reads=%v deleted=%v", repo.ids, repo.deletedIDs)
			}
		})
	}
}

func TestDeleteAccountErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code                  string
		unauthenticated, inactive   bool
		authErr, findErr, deleteErr error
		status, reads, deletes      int
	}{
		{name: "missing identity", unauthenticated: true, status: 401, code: "authentication_required"},
		{name: "not provisioned", authErr: service.ErrNotFound, status: 403, code: "account_not_provisioned"},
		{name: "inactive authorization", authErr: service.ErrInactive, status: 403, code: "account_inactive"},
		{name: "authorization unavailable", authErr: service.ErrUnavailable, status: 503, code: "account_unavailable"},
		{name: "account missing", findErr: service.ErrNotFound, status: 403, code: "account_not_provisioned", reads: 1},
		{name: "account inactive", inactive: true, status: 403, code: "account_inactive", reads: 1},
		{name: "read failure", findErr: errors.New("private database detail"), status: 503, code: "account_unavailable", reads: 1},
		{name: "delete failure", deleteErr: errors.New("private database detail"), status: 503, code: "account_unavailable", reads: 1, deletes: 1},
		{name: "concurrent deactivation", deleteErr: service.ErrInactive, status: 403, code: "account_inactive", reads: 1, deletes: 1},
		{name: "wrapped validation", authErr: fmt.Errorf("lookup: %w", &service.ValidationError{Field: "identity", Message: "required"}), status: 400, code: "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &deleteRepositoryStub{profileRepositoryStub: profileRepositoryStub{user: repository.User{Active: !tc.inactive}, findErr: tc.findErr}, deleteErr: tc.deleteErr}
			pro := &provisionerStub{accountID: "local-id", authorizeErr: tc.authErr}
			res := httptest.NewRecorder()
			deleteAccount(res, accountRequest(http.MethodDelete, `{"delete_confirmation":true}`, !tc.unauthenticated), pro, service.NewUserService(repo, nil))
			assertDeleteResponse(t, res, tc.status, tc.code)
			if repo.reads != tc.reads || len(repo.deletedIDs) != tc.deletes {
				t.Fatalf("reads=%d deletes=%d; want %d, %d", repo.reads, len(repo.deletedIDs), tc.reads, tc.deletes)
			}
			if tc.unauthenticated && pro.authorizeCalls != 0 {
				t.Fatal("unauthenticated request reached account lookup")
			}
		})
	}
}

func assertDeleteResponse(t *testing.T, res *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if res.Code != status {
		t.Fatalf("status=%d want=%d body=%s", res.Code, status, res.Body.String())
	}
	if res.Header().Get("Content-Type") != "application/json" || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers=%v", res.Header())
	}
	if code != "" {
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error != code || body.Message == "" || strings.Contains(res.Body.String(), "private database detail") {
			t.Fatalf("unexpected error response: %s", res.Body.String())
		}
	}
}
