package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

const testSupplierID supplier.SupplierID = "29e9aa8b-5651-4981-8828-3fbb1b21fcb1"

type readerStub struct {
	page   supplier.Page
	item   supplier.Supplier
	err    error
	calls  int
	filter supplier.ListFilter
	id     supplier.SupplierID
	ctx    context.Context
}

func (r *readerStub) ListAvailable(ctx context.Context, filter supplier.ListFilter) (supplier.Page, error) {
	r.calls++
	r.ctx, r.filter = ctx, filter
	return r.page, r.err
}

func (r *readerStub) GetCurrentAvailable(ctx context.Context, id supplier.SupplierID) (supplier.Supplier, error) {
	r.calls++
	r.ctx, r.id = ctx, id
	return r.item, r.err
}

func (r *readerStub) GetVersion(context.Context, supplier.VersionID) (supplier.Version, error) {
	panic("catalogue must use current-only reads")
}

func TestListNormalizesSearchAndTypeAndDefaultsPageSize(t *testing.T) {
	reader := &readerStub{page: supplier.Page{Items: []supplier.Supplier{catalogueItem()}, NextCursor: "opaque"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page, err := NewService(reader).List(ctx, auth.Principal{Subject: "user-1", Permissions: []auth.Permission{auth.ReadSuppliers}}, supplier.ListFilter{
		Query: "  COM\t  3 \n", Type: " FoOd ", Cursor: "previous",
	})
	require.NoError(t, err)
	require.Equal(t, reader.page, page)
	require.Equal(t, supplier.ListFilter{Query: "com 3", Type: "food", Limit: 25, Cursor: "previous"}, reader.filter)
	require.Equal(t, ctx, reader.ctx)
}

func TestListRejectsInvalidFiltersBeforeReading(t *testing.T) {
	for _, filter := range []supplier.ListFilter{
		{Limit: -1}, {Limit: 101}, {Query: strings.Repeat("界", 121)},
		{Type: strings.Repeat("x", 65)}, {Cursor: strings.Repeat("x", 513)},
	} {
		reader := &readerStub{}
		_, err := NewService(reader).List(context.Background(), auth.Principal{Subject: "user-1", Permissions: []auth.Permission{auth.ReadSuppliers}}, filter)
		var validation *supplier.ValidationError
		require.ErrorAs(t, err, &validation)
		require.Zero(t, reader.calls)
	}
}

func TestCatalogueServicesRequireReadPermission(t *testing.T) {
	reader := &readerStub{}
	service := NewService(reader)
	_, err := service.List(context.Background(), auth.Principal{}, supplier.ListFilter{})
	var application *apperror.Error
	require.ErrorAs(t, err, &application)
	require.Equal(t, apperror.Unauthenticated, application.Code)
	_, err = service.Current(context.Background(), auth.Principal{Roles: []auth.Role{auth.RoleAdministrator}, Permissions: []auth.Permission{auth.ReadSuppliers, auth.ManageSuppliers}}, testSupplierID)
	require.ErrorAs(t, err, &application)
	require.Equal(t, apperror.Unauthenticated, application.Code)
	require.Zero(t, reader.calls)
}

func TestCurrentUsesCurrentOnlyReadAndValidatesID(t *testing.T) {
	reader := &readerStub{item: catalogueItem()}
	service := NewService(reader)
	principal := auth.Principal{Subject: "admin-1", Roles: []auth.Role{auth.RoleAdministrator}, Permissions: []auth.Permission{auth.ReadSuppliers, auth.ManageSuppliers}}
	ctx := context.Background()
	item, err := service.Current(ctx, principal, testSupplierID)
	require.NoError(t, err)
	require.Equal(t, reader.item, item)
	require.Equal(t, testSupplierID, reader.id)
	require.Equal(t, ctx, reader.ctx)
	_, err = service.Current(ctx, principal, "invalid")
	var application *apperror.Error
	require.ErrorAs(t, err, &application)
	require.Equal(t, apperror.InvalidArgument, application.Code)
	require.Equal(t, 1, reader.calls)
}

func TestCatalogueServicesPropagateRepositoryErrors(t *testing.T) {
	for _, err := range []error{errors.New("storage failed"), &apperror.Error{Code: apperror.SupplierNotFound}} {
		reader := &readerStub{err: err}
		service := NewService(reader)
		principal := auth.Principal{Subject: "user-1", Permissions: []auth.Permission{auth.ReadSuppliers}}
		_, actual := service.List(context.Background(), principal, supplier.ListFilter{})
		require.ErrorIs(t, actual, err)
		_, actual = service.Current(context.Background(), principal, testSupplierID)
		require.ErrorIs(t, actual, err)
	}
}
