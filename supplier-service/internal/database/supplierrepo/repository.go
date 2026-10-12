// Package supplierrepo persists versioned suppliers and guarded deletion
// operations in PostgreSQL.
package supplierrepo

import (
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	db "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/generated"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

const normalizedNameConstraint = "suppliers_live_normalized_name_uidx"

// lookupConstraintViolations maps lookup constraints to the field they
// guard. Validation normally reports these first; the mapping covers a lookup
// that changes between validation and the write.
var lookupConstraintViolations = map[string]supplier.FieldViolation{
	"supplier_versions_building_fkey":      unknownBuilding,
	"supplier_versions_supplier_type_fkey": unknownSupplierType,
	"buildings_name_not_blank":             {Field: "building", Message: "is required"},
	"buildings_lookup_key_not_blank":       {Field: "building", Message: "must contain a letter or digit"},
	"supplier_types_name_not_blank":        {Field: "type", Message: "is required"},
	"supplier_types_lookup_key_not_blank":  {Field: "type", Message: "must contain a letter or digit"},
}

type Repository struct {
	pool         *pgxpool.Pool
	queries      *db.Queries
	logger       *slog.Logger
	cursorCipher cipher.AEAD
}

func New(pool *pgxpool.Pool, loggers ...*slog.Logger) *Repository {
	var logger *slog.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	if logger != nil {
		logger.Info("supplier repository initialized", "component", "supplier_repository")
	}
	return &Repository{pool: pool, queries: db.New(pool), logger: logger, cursorCipher: newCursorCipher()}
}

var _ supplier.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, details supplier.Details, now time.Time) (result supplier.Supplier, err error) {
	defer r.observe("create", time.Now(), &err)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return supplier.Supplier{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := r.queries.WithTx(tx)
	// Input lookup IDs are ignored: building and type must name existing,
	// unretired lookups, resolved by key inside this transaction.
	details.BuildingID, details.TypeID = "", ""
	details, err = validateAndResolve(ctx, queries, details)
	if err != nil {
		return supplier.Supplier{}, err
	}
	identity, err := queries.InsertSupplierIdentity(ctx, db.InsertSupplierIdentityParams{
		NormalizedName: supplier.NormalizeName(details.Name),
		Now:            timestamp(now),
	})
	if err != nil {
		return supplier.Supplier{}, mapWriteError(err)
	}
	if err := queries.InsertSupplierVersion(ctx, versionParams(identity.VersionID, identity.SupplierID, details, now)); err != nil {
		return supplier.Supplier{}, mapWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return supplier.Supplier{}, mapWriteError(err)
	}

	return supplier.Supplier{
		SupplierID: supplier.SupplierID(identity.SupplierID),
		VersionID:  supplier.VersionID(identity.VersionID),
		Details:    details,
		Available:  true,
		CreatedAt:  identity.CreatedAt.Time,
		UpdatedAt:  identity.UpdatedAt.Time,
	}, nil
}

func (r *Repository) Update(ctx context.Context, supplierID supplier.SupplierID, patch supplier.Patch, now time.Time) (result supplier.Supplier, err error) {
	defer r.observe("update", time.Now(), &err)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return supplier.Supplier{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := r.queries.WithTx(tx)
	current, err := queries.LockCurrentSupplier(ctx, string(supplierID))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.Supplier{}, supplierNotFound()
	}
	if err != nil {
		return supplier.Supplier{}, err
	}
	currentVersion, err := queries.GetSupplierVersion(ctx, current.VersionID)
	if err != nil {
		return supplier.Supplier{}, err
	}

	// The row lock deliberately precedes patch application and validation. A
	// concurrent patch therefore observes the version committed immediately
	// before it instead of validating against a stale snapshot.
	details := patch.Apply(detailsFromValues(
		currentVersion.Name,
		currentVersion.SupplierType,
		currentVersion.SupplierTypeID,
		currentVersion.Building,
		currentVersion.BuildingID,
		currentVersion.Floor,
		currentVersion.LocationDescription,
		currentVersion.Latitude,
		currentVersion.Longitude,
		currentVersion.OpeningTime,
		currentVersion.ClosingTime,
		currentVersion.ImageUrl,
	))
	// Apply cleared the ID of a patched building or type, so only those are
	// resolved and must name an existing, unretired lookup. An unpatched
	// lookup keeps its row even if it has since been retired, so the
	// supplier's other fields stay editable.
	details, err = validateAndResolve(ctx, queries, details)
	if err != nil {
		return supplier.Supplier{}, err
	}

	versionID, err := queries.GenerateSupplierVersionID(ctx)
	if err != nil {
		return supplier.Supplier{}, err
	}
	if err := queries.InsertSupplierVersion(ctx, versionParams(versionID, string(supplierID), details, now)); err != nil {
		return supplier.Supplier{}, mapWriteError(err)
	}
	identity, err := queries.ReplaceCurrentSupplierVersion(ctx, db.ReplaceCurrentSupplierVersionParams{
		VersionID:      versionID,
		NormalizedName: supplier.NormalizeName(details.Name),
		Now:            timestamp(now),
		SupplierID:     string(supplierID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.Supplier{}, supplierNotFound()
	}
	if err != nil {
		return supplier.Supplier{}, mapWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return supplier.Supplier{}, mapWriteError(err)
	}

	return supplier.Supplier{
		SupplierID: supplierID,
		VersionID:  supplier.VersionID(versionID),
		Details:    details,
		Available:  true,
		CreatedAt:  identity.CreatedAt.Time,
		UpdatedAt:  identity.UpdatedAt.Time,
	}, nil
}

func (r *Repository) GetCurrentAvailable(ctx context.Context, supplierID supplier.SupplierID) (result supplier.Supplier, err error) {
	defer r.observe("get_current", time.Now(), &err)
	row, err := r.queries.GetCurrentAvailableSupplier(ctx, string(supplierID))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.Supplier{}, supplierNotFound()
	}
	if err != nil {
		return supplier.Supplier{}, err
	}
	return supplier.Supplier{
		SupplierID: supplier.SupplierID(row.SupplierID),
		VersionID:  supplier.VersionID(row.VersionID),
		Details: detailsFromValues(
			row.Name,
			row.SupplierType,
			row.SupplierTypeID,
			row.Building,
			row.BuildingID,
			row.Floor,
			row.LocationDescription,
			row.Latitude,
			row.Longitude,
			row.OpeningTime,
			row.ClosingTime,
			row.ImageUrl,
		),
		Available: true,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (r *Repository) GetVersion(ctx context.Context, versionID supplier.VersionID) (result supplier.Version, err error) {
	defer r.observe("get_version", time.Now(), &err)
	row, err := r.queries.GetSupplierVersion(ctx, string(versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.Version{}, supplierVersionNotFound()
	}
	if err != nil {
		return supplier.Version{}, err
	}
	return supplier.Version{
		SupplierID: supplier.SupplierID(row.SupplierID),
		VersionID:  supplier.VersionID(row.VersionID),
		Details: detailsFromValues(
			row.Name,
			row.SupplierType,
			row.SupplierTypeID,
			row.Building,
			row.BuildingID,
			row.Floor,
			row.LocationDescription,
			row.Latitude,
			row.Longitude,
			row.OpeningTime,
			row.ClosingTime,
			row.ImageUrl,
		),
		Available: row.Available.Valid && row.Available.Bool,
		CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (r *Repository) ListAvailable(ctx context.Context, filter supplier.ListFilter) (result supplier.Page, err error) {
	defer r.observe("list_available", time.Now(), &err)
	if err := supplier.ValidateListFilter(filter); err != nil {
		return supplier.Page{}, err
	}

	params := db.ListAvailableSuppliersByNameParams{
		QueryText:    supplier.NormalizeName(filter.Query),
		SupplierType: strings.TrimSpace(filter.Type),
		Building:     strings.TrimSpace(filter.Building),
		HasCursor:    filter.Cursor != "",
		PageLimit:    int32(filter.PageSize() + 1),
	}
	var anchorDetails supplier.Details
	if filter.Cursor != "" {
		versionID, err := r.decodeCursor(filter.Cursor, filter)
		if err != nil {
			return supplier.Page{}, err
		}
		// The immutable anchor version supplies every sort-key column, so the
		// cursor stays valid after its supplier is renamed or deleted.
		anchor, err := r.GetVersion(ctx, versionID)
		if err != nil {
			var application *apperror.Error
			if errors.As(err, &application) && application.Code == apperror.SupplierVersionNotFound {
				return supplier.Page{}, invalidArgument("cursor is invalid")
			}
			return supplier.Page{}, err
		}
		params.CursorName, params.CursorSupplierID = supplier.NormalizeName(anchor.Details.Name), string(anchor.SupplierID)
		anchorDetails = anchor.Details
	}
	rows, err := r.listAvailableRows(ctx, filter.SortOrder(), params, anchorDetails)
	if err != nil {
		return supplier.Page{}, err
	}

	pageSize := filter.PageSize()
	page := supplier.Page{Items: make([]supplier.Supplier, 0, min(len(rows), pageSize))}
	for _, row := range rows[:min(len(rows), pageSize)] {
		page.Items = append(page.Items, supplier.Supplier{
			SupplierID: supplier.SupplierID(row.SupplierID),
			VersionID:  supplier.VersionID(row.VersionID),
			Details: detailsFromValues(
				row.Name,
				row.SupplierType,
				row.SupplierTypeID,
				row.Building,
				row.BuildingID,
				row.Floor,
				row.LocationDescription,
				row.Latitude,
				row.Longitude,
				row.OpeningTime,
				row.ClosingTime,
				row.ImageUrl,
			),
			Available: true,
			CreatedAt: row.CreatedAt.Time,
			UpdatedAt: row.UpdatedAt.Time,
		})
	}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.NextCursor = r.encodeCursor(supplier.VersionID(last.VersionID), filter)
	}
	return page, nil
}

// listAvailableRows dispatches to the per-sort query. Type and building sorts
// receive the anchor's names; SQL converts them to lookup keys like the sort
// key.
func (r *Repository) listAvailableRows(ctx context.Context, sort supplier.ListSort, params db.ListAvailableSuppliersByNameParams, anchor supplier.Details) ([]db.ListAvailableSuppliersByNameRow, error) {
	typeParams := db.ListAvailableSuppliersByTypeParams{
		QueryText:        params.QueryText,
		SupplierType:     params.SupplierType,
		Building:         params.Building,
		HasCursor:        params.HasCursor,
		CursorType:       anchor.Type,
		CursorName:       params.CursorName,
		CursorSupplierID: params.CursorSupplierID,
		PageLimit:        params.PageLimit,
	}
	buildingParams := db.ListAvailableSuppliersByBuildingParams{
		QueryText:        params.QueryText,
		SupplierType:     params.SupplierType,
		Building:         params.Building,
		HasCursor:        params.HasCursor,
		CursorBuilding:   anchor.Building,
		CursorName:       params.CursorName,
		CursorSupplierID: params.CursorSupplierID,
		PageLimit:        params.PageLimit,
	}
	switch sort {
	case supplier.SortName:
		return r.queries.ListAvailableSuppliersByName(ctx, params)
	case supplier.SortNameDesc:
		return nameRows(r.queries.ListAvailableSuppliersByNameDesc(ctx, db.ListAvailableSuppliersByNameDescParams(params)))
	case supplier.SortType:
		return nameRows(r.queries.ListAvailableSuppliersByType(ctx, typeParams))
	case supplier.SortTypeDesc:
		return nameRows(r.queries.ListAvailableSuppliersByTypeDesc(ctx, db.ListAvailableSuppliersByTypeDescParams(typeParams)))
	case supplier.SortBuilding:
		return nameRows(r.queries.ListAvailableSuppliersByBuilding(ctx, buildingParams))
	case supplier.SortBuildingDesc:
		return nameRows(r.queries.ListAvailableSuppliersByBuildingDesc(ctx, db.ListAvailableSuppliersByBuildingDescParams(buildingParams)))
	}
	return nil, invalidArgument("sort is invalid")
}

// availableRow is the set of generated list row types. They share one
// projection, so each converts to the name-ordered row type.
type availableRow interface {
	db.ListAvailableSuppliersByNameDescRow |
		db.ListAvailableSuppliersByTypeRow |
		db.ListAvailableSuppliersByTypeDescRow |
		db.ListAvailableSuppliersByBuildingRow |
		db.ListAvailableSuppliersByBuildingDescRow
}

func nameRows[T availableRow](rows []T, err error) ([]db.ListAvailableSuppliersByNameRow, error) {
	converted := make([]db.ListAvailableSuppliersByNameRow, len(rows))
	for i, row := range rows {
		converted[i] = db.ListAvailableSuppliersByNameRow(row)
	}
	return converted, err
}

func (r *Repository) NormalizedNameExists(ctx context.Context, name string, excluded *supplier.SupplierID) (exists bool, err error) {
	defer r.observe("normalized_name_exists", time.Now(), &err)
	excludedID := ""
	if excluded != nil {
		excludedID = string(*excluded)
	}
	return r.queries.NormalizedSupplierNameExists(ctx, db.NormalizedSupplierNameExistsParams{
		NormalizedName:    supplier.NormalizeName(name),
		ExcludeSupplierID: excludedID,
	})
}

func versionParams(versionID, supplierID string, details supplier.Details, now time.Time) db.InsertSupplierVersionParams {
	imageURL := ""
	if details.ImageURL != nil {
		imageURL = *details.ImageURL
	}
	return db.InsertSupplierVersionParams{
		VersionID:           versionID,
		SupplierID:          supplierID,
		Name:                details.Name,
		SupplierTypeID:      details.TypeID,
		BuildingID:          details.BuildingID,
		Floor:               details.Floor,
		LocationDescription: details.LocationDescription,
		Latitude:            details.Latitude,
		Longitude:           details.Longitude,
		OpeningTime:         details.OpeningTime,
		ClosingTime:         details.ClosingTime,
		ImageUrl:            imageURL,
		Now:                 timestamp(now),
	}
}

func detailsFromValues(name, supplierType, typeID, building, buildingID, floor, location string, latitude, longitude float64, opening, closing, image string) supplier.Details {
	var imageURL *string
	if image != "" {
		imageURL = &image
	}
	return supplier.Details{
		Name:                name,
		Type:                supplierType,
		TypeID:              typeID,
		Building:            building,
		BuildingID:          buildingID,
		Floor:               floor,
		LocationDescription: location,
		Latitude:            latitude,
		Longitude:           longitude,
		OpeningTime:         opening,
		ClosingTime:         closing,
		ImageURL:            imageURL,
	}
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func nullableTimestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return timestamp(*value)
}

func mapWriteError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return err
	}
	if postgresError.Code == "23505" && postgresError.ConstraintName == normalizedNameConstraint {
		return apperror.ErrSupplierNameConflict
	}
	if postgresError.Code == "23503" || postgresError.Code == "23514" {
		if violation, found := lookupConstraintViolations[postgresError.ConstraintName]; found {
			return &supplier.ValidationError{Violations: []supplier.FieldViolation{violation}}
		}
	}
	return err
}

func supplierNotFound() error {
	return &apperror.Error{Code: apperror.SupplierNotFound, Message: "supplier not found"}
}

func supplierVersionNotFound() error {
	return &apperror.Error{Code: apperror.SupplierVersionNotFound, Message: "supplier version not found"}
}

func invalidArgument(message string) error {
	return &apperror.Error{Code: apperror.InvalidArgument, Message: message}
}

func boundedInt32(value int, field string) (int32, error) {
	if value <= 0 || value > math.MaxInt32 {
		return 0, invalidArgument(fmt.Sprintf("%s must be positive", field))
	}
	return int32(value), nil
}
