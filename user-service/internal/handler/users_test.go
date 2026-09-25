package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/auth0/go-jwt-middleware/v3/core"
	"github.com/auth0/go-jwt-middleware/v3/validator"
	"user-service/internal/repository"
	"user-service/internal/service"
)

type profileRepositoryStub struct {
	user          repository.User
	findErr       error
	updateErr     error
	reads, writes int
	ids           []string
}

func (s *profileRepositoryStub) FindByID(_ context.Context, id string) (*repository.User, error) {
	s.reads++
	s.ids = append(s.ids, id)
	return &s.user, s.findErr
}
func (s *profileRepositoryStub) UpdateProfile(_ context.Context, id string, patch repository.ProfilePatch) (*repository.User, error) {
	s.writes++
	s.ids = append(s.ids, id)
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if patch.DisplayName != nil {
		s.user.DisplayName = *patch.DisplayName
	}
	if patch.Mobile != nil {
		s.user.Mobile = *patch.Mobile
	}
	return &s.user, nil
}
func (s *profileRepositoryStub) SoftDelete(context.Context, string) error {
	panic("profile endpoints must not delete accounts")
}

func accountRequest(method, body string, authenticated bool) *http.Request {
	req := httptest.NewRequest(method, "/api/me?id=another-account", strings.NewReader(body))
	if authenticated {
		req = req.WithContext(core.SetClaims(req.Context(), &validator.ValidatedClaims{
			RegisteredClaims: validator.RegisteredClaims{Subject: "auth0|student"},
		}))
	}
	return req
}

func TestAccountProfileReadAndUpdate(t *testing.T) {
	for _, tc := range []struct{ name, method, body, display, mobile string }{
		{"read", "GET", "", "Before", "123"},
		{"name only", "PATCH", `{"display_name":"Alex"}`, "Alex", "123"},
		{"phone only", "PATCH", `{"mobile_number":"456"}`, "Before", "456"},
		{"both", "PATCH", `{"display_name":"Alex","mobile_number":"456"}`, "Alex", "456"},
		{"clear", "PATCH", `{"mobile_number":""}`, "Before", ""},
		{"null preserves", "PATCH", `{"display_name":"Alex","mobile_number":null}`, "Alex", "123"},
		{"trailing whitespace", "PATCH", "{\"display_name\":\"Alex\"} \n\t", "Alex", "123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &profileRepositoryStub{user: repository.User{ID: "local-id", Username: "student", Email: "student@u.nus.edu", Role: "USER", Active: true, DisplayName: "Before", Mobile: "123"}}
			pro := &provisionerStub{accountID: "local-id"}
			users := service.NewUserService(repo, nil)
			res := httptest.NewRecorder()
			req := accountRequest(tc.method, tc.body, true)
			if tc.method == "GET" {
				getAccountInformation(res, req, pro, users)
			} else {
				updateAccountInformation(res, req, pro, users)
			}
			if res.Code != 200 {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
			var got service.Profile
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.ID != "local-id" || got.Username != "student" || got.Email != "student@u.nus.edu" || got.Role != "USER" || !got.Active || got.DisplayName != tc.display || got.Mobile != tc.mobile || got.CreditBalance != nil {
				t.Fatalf("unexpected profile: %+v", got)
			}
			if res.Header().Get("Content-Type") != "application/json" || res.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("headers=%v", res.Header())
			}
			if pro.subject != "auth0|student" {
				t.Fatalf("subject=%s", pro.subject)
			}
			for _, id := range repo.ids {
				if id != "local-id" {
					t.Fatalf("untrusted account ID: %s", id)
				}
			}
			wantWrites := 1
			if tc.method == "GET" {
				wantWrites = 0
			}
			if repo.writes != wantWrites {
				t.Fatalf("writes=%d", repo.writes)
			}
		})
	}
}

func TestProfileUpdateRejectsInvalidBodies(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"role", `{"role":"ADMIN"}`, 400},
		{"balance", `{"credit_balance":"100"}`, 400},
		{"email", `{"email":"other@u.nus.edu"}`, 400},
		{"id", `{"id":"another-account"}`, 400},
		{"mixed protected", `{"display_name":"Alex","active":false}`, 400},
		{"empty", "", 400}, {"empty object", `{}`, 400}, {"null", `null`, 400},
		{"array", `[]`, 400}, {"wrong type", `{"display_name":123}`, 400},
		{"malformed", `{"display_name":`, 400},
		{"second object", `{"display_name":"Alex"}{}`, 400},
		{"second scalar", `{"display_name":"Alex"}true`, 400},
		{"trailing garbage", `{"display_name":"Alex"}garbage`, 400},
		{"name too long", `{"display_name":"` + strings.Repeat("x", 101) + `"}`, 400},
		{"phone too long", `{"mobile_number":"` + strings.Repeat("x", 33) + `"}`, 400},
		{"control character", `{"display_name":"A\nB"}`, 400},
		{"oversized value", `{"display_name":"` + strings.Repeat("x", 4096) + `"}`, 413},
		{"oversized trailing whitespace", `{"display_name":"Alex"}` + strings.Repeat(" ", 4096), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &profileRepositoryStub{user: repository.User{Active: true}}
			res := httptest.NewRecorder()
			updateAccountInformation(res, accountRequest("PATCH", tc.body, true), &provisionerStub{accountID: "local-id"}, service.NewUserService(repo, nil))
			if res.Code != tc.status || repo.writes != 0 {
				t.Fatalf("status=%d writes=%d body=%s", res.Code, repo.writes, res.Body.String())
			}
		})
	}
}

func TestAccountEndpointErrors(t *testing.T) {
	for _, method := range []string{"GET", "PATCH"} {
		for _, tc := range []struct {
			name             string
			unauthenticated  bool
			authErr, findErr error
			status           int
			code             string
		}{
			{name: "missing identity", unauthenticated: true, status: 401, code: "authentication_required"},
			{name: "not provisioned", authErr: service.ErrNotFound, status: 403, code: "account_not_provisioned"},
			{name: "inactive", authErr: service.ErrInactive, status: 403, code: "account_inactive"},
			{name: "lookup unavailable", authErr: service.ErrUnavailable, status: 503, code: "account_unavailable"},
			{name: "profile missing", findErr: service.ErrNotFound, status: 403, code: "account_not_provisioned"},
			{name: "profile read failure", findErr: errors.New("private database detail"), status: 503, code: "account_unavailable"},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				repo := &profileRepositoryStub{user: repository.User{Active: true}, findErr: tc.findErr}
				pro := &provisionerStub{accountID: "local-id", authorizeErr: tc.authErr}
				res := httptest.NewRecorder()
				req := accountRequest(method, `{"display_name":"Alex"}`, !tc.unauthenticated)
				users := service.NewUserService(repo, nil)
				if method == "GET" {
					getAccountInformation(res, req, pro, users)
				} else {
					updateAccountInformation(res, req, pro, users)
				}
				if res.Code != tc.status || !strings.Contains(res.Body.String(), tc.code) || strings.Contains(res.Body.String(), "private database detail") || repo.writes != 0 {
					t.Fatalf("status=%d writes=%d body=%s", res.Code, repo.writes, res.Body.String())
				}
				if tc.unauthenticated && pro.authorizeCalls != 0 {
					t.Fatal("unauthenticated request reached account lookup")
				}
				if (tc.unauthenticated || tc.authErr != nil) && repo.reads != 0 {
					t.Fatal("rejected request reached profile repository")
				}
			})
		}
	}
}

func TestProfileUpdatePersistenceFailure(t *testing.T) {
	repo := &profileRepositoryStub{user: repository.User{Active: true}, updateErr: errors.New("private database detail")}
	res := httptest.NewRecorder()
	updateAccountInformation(res, accountRequest("PATCH", `{"display_name":"Alex"}`, true), &provisionerStub{accountID: "local-id"}, service.NewUserService(repo, nil))
	if res.Code != 503 || repo.writes != 1 || strings.Contains(res.Body.String(), "private database detail") {
		t.Fatalf("status=%d writes=%d body=%s", res.Code, repo.writes, res.Body.String())
	}
}
