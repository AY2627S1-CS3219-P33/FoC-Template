// Package supplierrepo persists versioned suppliers and guarded deletion
// operations in PostgreSQL.
package supplierrepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type Repository struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: db.New(pool)}
}

var _ supplier.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, details supplier.Details, now time.Time) (supplier.Supplier, error) {
	if err := supplier.ValidateDetails(details); err != nil {
		return supplier.Supplier{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return supplier.Supplier{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := r.queries.WithTx(tx)
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

func (r *Repository) Update(ctx context.Context, supplierID supplier.SupplierID, patch supplier.Patch, now time.Time) (supplier.Supplier, error) {
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
		currentVersion.Building,
		currentVersion.Floor,
		currentVersion.LocationDescription,
		currentVersion.Latitude,
		currentVersion.Longitude,
		currentVersion.OpeningTime,
		currentVersion.ClosingTime,
		currentVersion.ImageUrl,
	))
	if err := supplier.ValidateDetails(details); err != nil {
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

func (r *Repository) GetCurrentAvailable(ctx context.Context, supplierID supplier.SupplierID) (supplier.Supplier, error) {
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
			row.Building,
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

func (r *Repository) GetVersion(ctx context.Context, versionID supplier.VersionID) (supplier.Version, error) {
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
			row.Building,
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

func (r *Repository) ListAvailable(ctx context.Context, filter supplier.ListFilter) (supplier.Page, error) {
	if err := supplier.ValidateListFilter(filter); err != nil {
		return supplier.Page{}, err
	}

	cursor, err := decodeCursor(filter.Cursor)
	if err != nil {
		return supplier.Page{}, err
	}
	pageSize := filter.PageSize()
	rows, err := r.queries.ListAvailableSuppliers(ctx, db.ListAvailableSuppliersParams{
		QueryText:        strings.TrimSpace(filter.Query),
		SupplierType:     strings.TrimSpace(filter.Type),
		HasCursor:        filter.Cursor != "",
		CursorName:       cursor.NormalizedName,
		CursorSupplierID: cursor.SupplierID,
		PageLimit:        int32(pageSize + 1),
	})
	if err != nil {
		return supplier.Page{}, err
	}

	page := supplier.Page{Items: make([]supplier.Supplier, 0, min(len(rows), pageSize))}
	for _, row := range rows[:min(len(rows), pageSize)] {
		page.Items = append(page.Items, supplier.Supplier{
			SupplierID: supplier.SupplierID(row.SupplierID),
			VersionID:  supplier.VersionID(row.VersionID),
			Details: detailsFromValues(
				row.Name,
				row.SupplierType,
				row.Building,
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
		page.NextCursor, err = encodeCursor(listCursor{
			NormalizedName: last.NormalizedName,
			SupplierID:     last.SupplierID,
		})
		if err != nil {
			return supplier.Page{}, err
		}
	}
	return page, nil
}

func (r *Repository) NormalizedNameExists(ctx context.Context, name string, excluded *supplier.SupplierID) (bool, error) {
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
		SupplierType:        details.Type,
		Building:            details.Building,
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

func detailsFromValues(name, supplierType, building, floor, location string, latitude, longitude float64, opening, closing, image string) supplier.Details {
	var imageURL *string
	if image != "" {
		imageURL = &image
	}
	return supplier.Details{
		Name:                name,
		Type:                supplierType,
		Building:            building,
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
	if errors.As(err, &postgresError) &&
		postgresError.Code == "23505" &&
		postgresError.ConstraintName == normalizedNameConstraint {
		return apperror.ErrSupplierNameConflict
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

type listCursor struct {
	NormalizedName string `json:"name"`
	SupplierID     string `json:"supplierId"`
}

func encodeCursor(cursor listCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(value string) (listCursor, error) {
	if value == "" {
		return listCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return listCursor{}, invalidArgument("cursor is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var cursor listCursor
	if err := decoder.Decode(&cursor); err != nil {
		return listCursor{}, invalidArgument("cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return listCursor{}, invalidArgument("cursor is invalid")
	}
	if cursor.NormalizedName == "" || cursor.NormalizedName != supplier.NormalizeName(cursor.NormalizedName) ||
		!supplier.ValidSupplierID(supplier.SupplierID(cursor.SupplierID)) {
		return listCursor{}, invalidArgument("cursor is invalid")
	}
	return cursor, nil
}

func boundedInt32(value int, field string) (int32, error) {
	if value <= 0 || value > math.MaxInt32 {
		return 0, invalidArgument(fmt.Sprintf("%s must be positive", field))
	}
	return int32(value), nil
}
