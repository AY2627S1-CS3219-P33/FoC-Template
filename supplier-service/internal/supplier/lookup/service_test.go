package lookup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

var (
	reader = auth.Principal{Subject: "user-1", Permissions: []auth.Permission{auth.ReadSuppliers}}
	admin  = auth.Principal{Subject: "admin-1", Permissions: []auth.Permission{auth.ReadSuppliers, auth.ManageSuppliers}}
	now    = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
)

// fakeStore keeps one in-memory list per kind with the same live lookup-key
// uniqueness and retirement rules as PostgreSQL.
type fakeStore struct {
	rows  map[supplier.LookupKind][]supplier.Lookup
	users map[supplier.LookupID]int
	err   error
	calls int
	limit int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[supplier.LookupKind][]supplier.Lookup{}, users: map[supplier.LookupID]int{}}
}

func (s *fakeStore) add(kind supplier.LookupKind, name string, retired bool) supplier.Lookup {
	row := supplier.Lookup{ID: supplier.LookupID(fmt.Sprintf("00000000-0000-4000-8000-%012d", len(s.rows[kind])+1)), Name: name, Retired: retired}
	s.rows[kind] = append(s.rows[kind], row)
	return row
}

func (s *fakeStore) ListLookups(_ context.Context, kind supplier.LookupKind, includeRetired bool, limit int) ([]supplier.Lookup, error) {
	s.calls++
	s.limit = limit
	if s.err != nil {
		return nil, s.err
	}
	var result []supplier.Lookup
	for _, row := range s.rows[kind] {
		if includeRetired || !row.Retired {
			result = append(result, row)
		}
	}
	return result, nil
}

func (s *fakeStore) CreateLookup(_ context.Context, kind supplier.LookupKind, name string, _ time.Time) (supplier.Lookup, error) {
	s.calls++
	if s.err != nil {
		return supplier.Lookup{}, s.err
	}
	for _, row := range s.rows[kind] {
		if !row.Retired && supplier.LookupKey(row.Name) == supplier.LookupKey(name) {
			return supplier.Lookup{}, &supplier.LookupConflictError{Existing: row}
		}
	}
	return s.add(kind, name, false), nil
}

func (s *fakeStore) RetireLookup(_ context.Context, kind supplier.LookupKind, id supplier.LookupID, _ time.Time) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	for i, row := range s.rows[kind] {
		if row.ID != id {
			continue
		}
		if row.Retired {
			return nil
		}
		if s.users[id] > 0 {
			return &supplier.LookupInUseError{Lookup: row, Suppliers: s.users[id]}
		}
		s.rows[kind][i].Retired = true
		return nil
	}
	return supplier.ErrLookupNotFound
}

func requireCode(t *testing.T, err error, code apperror.Code) *apperror.Error {
	t.Helper()
	var application *apperror.Error
	require.ErrorAs(t, err, &application)
	require.Equal(t, code, application.Code)
	return application
}

func TestCreateValidatesTrimmedNameAgainstKindBounds(t *testing.T) {
	for _, test := range []struct {
		kind    Kind
		name    string
		message string
	}{
		{Buildings, "", "is required"},
		{Buildings, "   \t ", "is required"},
		{Buildings, strings.Repeat("界", 121), "is too long"},
		{SupplierTypes, strings.Repeat("x", 65), "is too long"},
		{Buildings, " --- ", "must contain a letter or digit"},
		{SupplierTypes, "//", "must contain a letter or digit"},
	} {
		store := newFakeStore()
		_, err := NewService(store).Create(context.Background(), admin, test.kind, test.name, now)
		validation := &supplier.ValidationError{}
		require.ErrorAs(t, err, &validation, test.name)
		require.Equal(t, []supplier.FieldViolation{{Field: "name", Message: test.message}}, validation.Violations)
		require.Zero(t, store.calls, "invalid names never reach the store")
	}

	store := newFakeStore()
	service := NewService(store)
	// Bounds count runes of the trimmed name, so padding does not count.
	created, err := service.Create(context.Background(), admin, Buildings, "  "+strings.Repeat("界", 120)+"  ", now)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("界", 120), created.Name)
	created, err = service.Create(context.Background(), admin, SupplierTypes, " "+strings.Repeat("x", 64)+" ", now)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("x", 64), created.Name)
}

func TestReadsNeedReadAndWritesNeedManage(t *testing.T) {
	store := newFakeStore()
	service := NewService(store)
	ctx := context.Background()
	anonymous := auth.Principal{}
	manageOnly := auth.Principal{Subject: "admin-2", Permissions: []auth.Permission{auth.ManageSuppliers}}
	noPermissions := auth.Principal{Subject: "user-2"}

	_, err := service.List(ctx, anonymous, Buildings, false)
	requireCode(t, err, apperror.Unauthenticated)
	_, err = service.List(ctx, noPermissions, Buildings, false)
	requireCode(t, err, apperror.Forbidden)
	for _, principal := range []auth.Principal{reader, manageOnly, noPermissions} {
		_, err = service.Create(ctx, principal, Buildings, "Com 2", now)
		requireCode(t, err, apperror.Forbidden)
		err = service.Retire(ctx, principal, SupplierTypes, "00000000-0000-4000-8000-000000000001", now)
		requireCode(t, err, apperror.Forbidden)
	}
	_, err = service.Create(ctx, anonymous, SupplierTypes, "Food", now)
	requireCode(t, err, apperror.Unauthenticated)
	err = service.Retire(ctx, anonymous, Buildings, "00000000-0000-4000-8000-000000000001", now)
	requireCode(t, err, apperror.Unauthenticated)
	require.Zero(t, store.calls)

	_, err = service.List(ctx, reader, SupplierTypes, false)
	require.NoError(t, err)
	_, err = service.Create(ctx, admin, SupplierTypes, "Food", now)
	require.NoError(t, err)
}

func TestCreateConflictsOnSameLookupKeyAndNamesTheExistingRow(t *testing.T) {
	store := newFakeStore()
	existing := store.add(supplier.BuildingLookup, "Com 2", false)
	service := NewService(store)

	_, err := service.Create(context.Background(), admin, Buildings, "Com2", now)
	conflict := requireCode(t, err, apperror.BuildingConflict)
	require.Contains(t, conflict.Message, `"Com 2"`)
	require.Contains(t, conflict.Message, string(existing.ID))

	store.add(supplier.SupplierTypeLookup, "Food & Drink", false)
	_, err = service.Create(context.Background(), admin, SupplierTypes, "food drink", now)
	conflict = requireCode(t, err, apperror.SupplierTypeConflict)
	require.Contains(t, conflict.Message, `"Food & Drink"`)

	// The winning row can be retired before it is read back.
	store.err = &supplier.LookupConflictError{}
	_, err = service.Create(context.Background(), admin, Buildings, "COM-2", now)
	conflict = requireCode(t, err, apperror.BuildingConflict)
	require.Equal(t, "a building with the same lookup key already exists", conflict.Message)
}

func TestCreateAfterRetirementReusesTheKey(t *testing.T) {
	store := newFakeStore()
	store.add(supplier.BuildingLookup, "Com 2", true)
	created, err := NewService(store).Create(context.Background(), admin, Buildings, "COM2", now)
	require.NoError(t, err)
	require.Equal(t, "COM2", created.Name)
}

func TestRetireMapsNotFoundInUseAndIsIdempotent(t *testing.T) {
	store := newFakeStore()
	service := NewService(store)
	ctx := context.Background()
	used := store.add(supplier.BuildingLookup, "Com 2", false)
	store.users[used.ID] = 3
	free := store.add(supplier.BuildingLookup, "Old Hall", false)

	err := service.Retire(ctx, admin, Buildings, used.ID, now)
	inUse := requireCode(t, err, apperror.BuildingInUse)
	require.Contains(t, inUse.Message, `"Com 2" is used by 3 supplier(s)`)
	require.False(t, store.rows[supplier.BuildingLookup][0].Retired)

	require.NoError(t, service.Retire(ctx, admin, Buildings, free.ID, now))
	require.NoError(t, service.Retire(ctx, admin, Buildings, free.ID, now), "retiring twice succeeds")
	require.True(t, store.rows[supplier.BuildingLookup][1].Retired)

	err = service.Retire(ctx, admin, Buildings, "00000000-0000-4000-8000-000000000099", now)
	requireCode(t, err, apperror.BuildingNotFound)
	err = service.Retire(ctx, admin, SupplierTypes, used.ID, now)
	requireCode(t, err, apperror.SupplierTypeNotFound)

	typeRow := store.add(supplier.SupplierTypeLookup, "Food", false)
	store.users[typeRow.ID] = 1
	err = service.Retire(ctx, admin, SupplierTypes, typeRow.ID, now)
	requireCode(t, err, apperror.SupplierTypeInUse)

	calls := store.calls
	err = service.Retire(ctx, admin, Buildings, "not-a-uuid", now)
	invalid := requireCode(t, err, apperror.InvalidArgument)
	require.Equal(t, "buildingId must be a UUID", invalid.Message)
	require.Equal(t, calls, store.calls)
}

func TestListRequestsCapAndRetiredRowsOnlyWhenAsked(t *testing.T) {
	store := newFakeStore()
	store.add(supplier.BuildingLookup, "Com 2", false)
	store.add(supplier.BuildingLookup, "Old Hall", true)
	service := NewService(store)

	live, err := service.List(context.Background(), reader, Buildings, false)
	require.NoError(t, err)
	require.Equal(t, []string{"Com 2"}, names(live))
	require.Equal(t, supplier.MaximumLookupListSize, store.limit)

	all, err := service.List(context.Background(), reader, Buildings, true)
	require.NoError(t, err)
	require.Equal(t, []string{"Com 2", "Old Hall"}, names(all))
	require.True(t, all[1].Retired)
}

func TestStoreFailuresPassThroughUnchanged(t *testing.T) {
	failure := errors.New("database password is secret")
	store := newFakeStore()
	store.err = failure
	_, err := NewService(store).List(context.Background(), reader, SupplierTypes, false)
	require.ErrorIs(t, err, failure)
}

func names(lookups []supplier.Lookup) []string {
	result := make([]string, len(lookups))
	for i, item := range lookups {
		result[i] = item.Name
	}
	return result
}
