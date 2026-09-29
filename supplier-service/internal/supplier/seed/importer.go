// Package seed imports the deployment-provided supplier CSV as one validated,
// idempotent dataset.
package seed

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

const maximumDatasetNamespaceLength = 120

var sourceTimePattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3])[0-5][0-9]hrs$`)

type Config struct {
	// CSVPath is deliberately required. The importer does not depend on the
	// process working directory or silently fall back to a development path.
	CSVPath string
	// DatasetNamespace is a stable, deployment-owned identifier for this
	// source dataset. Changing it intentionally creates a different dataset.
	DatasetNamespace string
}

// Record is a fully parsed and domain-validated seed row. SourceKey is the
// shared normalization of the original CSV name, not a supplier's mutable
// current name.
type Record struct {
	SourceKey string
	Details   supplier.Details
}

type ApplyResult struct {
	Inserted int
	Existing int
}

type Result struct {
	Rows     int
	Inserted int
	Existing int
}

// Store applies a complete parsed dataset atomically. Existing provenance
// keys must be skipped without reading or changing the current supplier.
type Store interface {
	Apply(context.Context, string, []Record, time.Time) (ApplyResult, error)
}

type Importer struct {
	config Config
	store  Store
	now    func() time.Time
}

func New(config Config, store Store) (*Importer, error) {
	if strings.TrimSpace(config.CSVPath) == "" {
		return nil, errors.New("supplier seed configuration: CSVPath is required")
	}
	config.DatasetNamespace = strings.TrimSpace(config.DatasetNamespace)
	if config.DatasetNamespace == "" {
		return nil, errors.New("supplier seed configuration: DatasetNamespace is required")
	}
	if utf8.RuneCountInString(config.DatasetNamespace) > maximumDatasetNamespaceLength {
		return nil, fmt.Errorf(
			"supplier seed configuration: DatasetNamespace must not exceed %d characters",
			maximumDatasetNamespaceLength,
		)
	}
	if store == nil {
		return nil, errors.New("supplier seed configuration: store is required")
	}
	return &Importer{
		config: config,
		store:  store,
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// Import reads and validates every row before asking the store to begin its
// transaction. Any returned error is safe to log or expose as a startup
// failure: paths, CSV values, database addresses, and credentials are omitted.
func (i *Importer) Import(ctx context.Context) (Result, error) {
	file, err := os.Open(i.config.CSVPath)
	if err != nil {
		return Result{}, newSafeError("configured seed CSV cannot be opened", err)
	}
	defer func() { _ = file.Close() }()

	records, err := parseCSV(file)
	if err != nil {
		return Result{}, err
	}
	applied, err := i.store.Apply(ctx, i.config.DatasetNamespace, records, i.now())
	if err != nil {
		return Result{}, newSafeError("seed rows could not be stored", err)
	}
	if applied.Inserted < 0 || applied.Existing < 0 || applied.Inserted+applied.Existing != len(records) {
		return Result{}, newSafeError("seed store returned an invalid result", nil)
	}
	return Result{
		Rows:     len(records),
		Inserted: applied.Inserted,
		Existing: applied.Existing,
	}, nil
}

var requiredColumns = []string{
	"Name",
	"Type",
	"Building",
	"Floor",
	"Location Description",
	"Latitude",
	"Longitude",
	"StartingTime",
	"ClosingTime",
}

func parseCSV(input io.Reader) ([]Record, error) {
	contents, err := io.ReadAll(input)
	if err != nil {
		return nil, newSafeError("seed CSV cannot be read", err)
	}
	if !utf8.Valid(contents) {
		contents, err = charmap.Windows1252.NewDecoder().Bytes(contents)
		if err != nil {
			return nil, newSafeError("seed CSV text encoding is invalid", err)
		}
	}

	reader := csv.NewReader(bytes.NewReader(contents))
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, newSafeError("seed CSV is empty", nil)
	}
	if err != nil {
		return nil, csvStructureError(err)
	}
	columns, err := columnIndexes(header)
	if err != nil {
		return nil, err
	}

	records := make([]Record, 0)
	firstSourceRows := make(map[string]int)
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, csvStructureError(readErr)
		}
		csvRow, _ := reader.FieldPos(0)
		record, parseErr := parseRow(row, columns, csvRow)
		if parseErr != nil {
			return nil, parseErr
		}
		if firstRow, duplicate := firstSourceRows[record.SourceKey]; duplicate {
			return nil, newSafeError(
				fmt.Sprintf("duplicate source key at CSV rows %d and %d", firstRow, csvRow),
				nil,
			)
		}
		firstSourceRows[record.SourceKey] = csvRow
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, newSafeError("seed CSV contains no data rows", nil)
	}
	return records, nil
}

func columnIndexes(header []string) (map[string]int, error) {
	columns := make(map[string]int, len(header))
	for index, rawName := range header {
		name := strings.TrimSpace(strings.TrimPrefix(rawName, "\ufeff"))
		if _, duplicate := columns[name]; duplicate {
			return nil, newSafeError("seed CSV header contains a duplicate column", nil)
		}
		columns[name] = index
	}
	for _, name := range requiredColumns {
		if _, found := columns[name]; !found {
			return nil, newSafeError("seed CSV is missing required column "+name, nil)
		}
	}
	return columns, nil
}

func parseRow(row []string, columns map[string]int, csvRow int) (Record, error) {
	latitude, err := coordinate(rowValue(row, columns, "Latitude"))
	if err != nil {
		return Record{}, rowFieldError(csvRow, "Latitude", "must be a number", err)
	}
	longitude, err := coordinate(rowValue(row, columns, "Longitude"))
	if err != nil {
		return Record{}, rowFieldError(csvRow, "Longitude", "must be a number", err)
	}
	openingTime, err := canonicalTime(rowValue(row, columns, "StartingTime"))
	if err != nil {
		return Record{}, rowFieldError(csvRow, "StartingTime", "must use HHMMhrs in 24-hour time", err)
	}
	closingTime, err := canonicalTime(rowValue(row, columns, "ClosingTime"))
	if err != nil {
		return Record{}, rowFieldError(csvRow, "ClosingTime", "must use HHMMhrs in 24-hour time", err)
	}

	var imageURL *string
	if value := strings.TrimSpace(rowValue(row, columns, "ImageURL")); value != "" {
		imageURL = &value
	}
	details := supplier.Details{
		Name:                strings.TrimSpace(rowValue(row, columns, "Name")),
		Type:                strings.TrimSpace(rowValue(row, columns, "Type")),
		Building:            strings.TrimSpace(rowValue(row, columns, "Building")),
		Floor:               strings.TrimSpace(rowValue(row, columns, "Floor")),
		LocationDescription: strings.TrimSpace(rowValue(row, columns, "Location Description")),
		Latitude:            latitude,
		Longitude:           longitude,
		OpeningTime:         openingTime,
		ClosingTime:         closingTime,
		ImageURL:            imageURL,
	}
	if err := supplier.ValidateDetails(details); err != nil {
		var validationError *supplier.ValidationError
		if !errors.As(err, &validationError) || len(validationError.Violations) == 0 {
			return Record{}, rowFieldError(csvRow, "row", "failed domain validation", err)
		}
		violation := validationError.Violations[0]
		return Record{}, rowFieldError(csvRow, violation.Field, violation.Message, err)
	}
	return Record{
		SourceKey: supplier.NormalizeName(details.Name),
		Details:   details,
	}, nil
}

func rowValue(row []string, columns map[string]int, name string) string {
	index, found := columns[name]
	if !found || index >= len(row) {
		return ""
	}
	return row[index]
}

func coordinate(value string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(value), 64)
}

func canonicalTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !sourceTimePattern.MatchString(value) {
		return "", errors.New("invalid source time")
	}
	return value[0:2] + ":" + value[2:4], nil
}

func csvStructureError(err error) error {
	var parseError *csv.ParseError
	if errors.As(err, &parseError) {
		return newSafeError(fmt.Sprintf("seed CSV structure is invalid near row %d", parseError.Line), err)
	}
	return newSafeError("seed CSV structure is invalid", err)
}

func rowFieldError(row int, field, message string, cause error) error {
	return newSafeError(
		fmt.Sprintf("seed CSV row %d field %s %s", row, field, message),
		cause,
	)
}

type safeError struct {
	message string
	cause   error
}

func newSafeError(message string, cause error) error {
	return &safeError{message: "supplier seed import: " + message, cause: cause}
}

func (e *safeError) Error() string {
	return e.message
}

func (e *safeError) Unwrap() error {
	return e.cause
}
