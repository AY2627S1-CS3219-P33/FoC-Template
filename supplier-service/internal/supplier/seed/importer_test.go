package seed

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func TestImporterParsesConfiguredRepositorySeedFile(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(
		filepath.Dir(sourceFile),
		"..", "..", "..", "..", "data", "csv", "supplier-seed-data.csv",
	)
	store := &recordingStore{}
	importer, err := New(Config{
		CSVPath:          path,
		DatasetNamespace: "foc-template-suppliers-v1",
	}, store)
	require.NoError(t, err)

	result, err := importer.Import(context.Background())
	require.NoError(t, err)
	require.Equal(t, Result{Rows: 21, Inserted: 21}, result)
	require.Len(t, store.records, 21)
	require.Equal(t, "anna's x soup union", store.records[0].SourceKey)
	require.Equal(t, "09:00", store.records[0].Details.OpeningTime)
	require.Equal(t, "18:00", store.records[0].Details.ClosingTime)
	require.Equal(t, "Prince George’s Park", store.records[12].Details.Building)
	require.Equal(t, "Prince George’s Park", store.records[17].Details.Building)
	require.Equal(t, "02:00", store.records[17].Details.ClosingTime)
	require.Nil(t, store.records[20].Details.ImageURL)
}

func TestImporterMapsCSVColumnsAndCanonicalizesTimes(t *testing.T) {
	path := writeSeedCSV(t, `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime,ImageURL
  Alpha   Cafe  ,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs,https://example.com/alpha.png
Beta Shop,Shopping,Central Library,2,Inside the library,1.296,103.772,0000hrs,2359hrs,
`)
	store := &recordingStore{}
	importer, err := New(Config{
		CSVPath:          path,
		DatasetNamespace: "template-v1",
	}, store)
	require.NoError(t, err)
	importer.now = func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) }

	result, err := importer.Import(context.Background())
	require.NoError(t, err)
	require.Equal(t, Result{Rows: 2, Inserted: 2}, result)
	require.Equal(t, 1, store.calls)
	require.Equal(t, "template-v1", store.namespace)
	require.Equal(t, importer.now(), store.seededAt)
	require.Equal(t, []Record{
		{
			SourceKey: "alpha cafe",
			Details: supplier.Details{
				Name:                "Alpha   Cafe",
				Type:                "Food",
				Building:            "COM 3",
				Floor:               "1",
				LocationDescription: "Atrium",
				Latitude:            1.294,
				Longitude:           103.773,
				OpeningTime:         "09:00",
				ClosingTime:         "18:00",
				ImageURL:            stringPointer("https://example.com/alpha.png"),
			},
		},
		{
			SourceKey: "beta shop",
			Details: supplier.Details{
				Name:                "Beta Shop",
				Type:                "Shopping",
				Building:            "Central Library",
				Floor:               "2",
				LocationDescription: "Inside the library",
				Latitude:            1.296,
				Longitude:           103.772,
				OpeningTime:         "00:00",
				ClosingTime:         "23:59",
			},
		},
	}, store.records)
}

func TestImporterRejectsDuplicateSourceKeysBeforeStore(t *testing.T) {
	path := writeSeedCSV(t, `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime
NUS   Co-op,Shopping,Central Library,1,Lobby,1.296,103.772,0900hrs,1800hrs
  nus co-OP  ,Shopping,Central Library,2,Second floor,1.296,103.772,1000hrs,1900hrs
`)
	store := &recordingStore{}
	importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, store)
	require.NoError(t, err)

	_, err = importer.Import(context.Background())
	require.ErrorContains(t, err, "duplicate source key")
	require.ErrorContains(t, err, "rows 2 and 3")
	require.NotContains(t, err.Error(), "NUS")
	require.Zero(t, store.calls)
}

func TestImporterRejectsInvalidRequiredRowWithoutPartialWrite(t *testing.T) {
	path := writeSeedCSV(t, `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime
Valid Supplier,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs
Sensitive Supplier,Food,COM 3,1,Atrium,not-a-coordinate,103.773,0900hrs,1800hrs
`)
	store := &recordingStore{}
	importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, store)
	require.NoError(t, err)

	_, err = importer.Import(context.Background())
	require.ErrorContains(t, err, "row 3")
	require.ErrorContains(t, err, "Latitude")
	require.NotContains(t, err.Error(), "Sensitive Supplier")
	require.NotContains(t, err.Error(), "not-a-coordinate")
	require.Zero(t, store.calls, "the complete CSV must validate before persistence starts")
}

func TestImporterRunsSharedDomainValidationBeforeStore(t *testing.T) {
	path := writeSeedCSV(t, `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime,ImageURL
Invalid Image,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs,ftp://example.com/image.png
`)
	store := &recordingStore{}
	importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, store)
	require.NoError(t, err)

	_, err = importer.Import(context.Background())
	require.ErrorContains(t, err, "field imageUrl")
	require.ErrorContains(t, err, "absolute HTTP or HTTPS URL")
	require.Zero(t, store.calls)
}

func TestImporterReturnsSecretFreeErrors(t *testing.T) {
	t.Run("configured path is not exposed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "password=swordfish", "missing.csv")
		importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, &recordingStore{})
		require.NoError(t, err)

		_, err = importer.Import(context.Background())
		require.ErrorContains(t, err, "configured seed CSV cannot be opened")
		require.NotContains(t, err.Error(), path)
		require.NotContains(t, err.Error(), "swordfish")
	})

	t.Run("persistence details are not exposed", func(t *testing.T) {
		path := writeSeedCSV(t, `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime
Alpha Cafe,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs
`)
		persistenceError := errors.New("postgresql://admin:secret@database/internal")
		store := &recordingStore{err: persistenceError}
		importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, store)
		require.NoError(t, err)

		_, err = importer.Import(context.Background())
		require.ErrorContains(t, err, "seed rows could not be stored")
		require.NotContains(t, err.Error(), "secret")
		require.NotContains(t, err.Error(), "database")
		require.ErrorIs(t, err, persistenceError)
	})
}

func TestImportLifecycleLogsAreStructuredAndRedacted(t *testing.T) {
	path := writeSeedCSV(t, `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime
Alpha Cafe,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs
`)
	secret := "postgresql://admin:secret@database/raw-record-payload"
	store := &recordingStore{err: errors.New(secret)}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, store, logger)
	require.NoError(t, err)

	_, err = importer.Import(context.Background())

	require.Error(t, err)
	require.Contains(t, output.String(), `"component":"supplier_seed"`)
	require.Contains(t, output.String(), `"msg":"supplier seed import started"`)
	require.Contains(t, output.String(), `"msg":"supplier seed import failed"`)
	require.Contains(t, output.String(), `"error":"supplier seed import: seed rows could not be stored"`)
	require.NotContains(t, output.String(), secret)
	require.NotContains(t, output.String(), "admin:secret")
}

func TestNewRequiresExplicitDeploymentConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		store  Store
		want   string
	}{
		{name: "CSV path", config: Config{DatasetNamespace: "template-v1"}, store: &recordingStore{}, want: "CSVPath is required"},
		{name: "dataset namespace", config: Config{CSVPath: "seed.csv"}, store: &recordingStore{}, want: "DatasetNamespace is required"},
		{name: "store", config: Config{CSVPath: "seed.csv", DatasetNamespace: "template-v1"}, want: "store is required"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.config, test.store)
			require.ErrorContains(t, err, test.want)
		})
	}
}

type recordingStore struct {
	calls     int
	namespace string
	records   []Record
	seededAt  time.Time
	result    ApplyResult
	err       error
}

func (s *recordingStore) Apply(
	_ context.Context,
	namespace string,
	records []Record,
	seededAt time.Time,
) (ApplyResult, error) {
	s.calls++
	s.namespace = namespace
	s.records = append([]Record(nil), records...)
	s.seededAt = seededAt
	if s.err != nil {
		return ApplyResult{}, s.err
	}
	if s.result == (ApplyResult{}) {
		return ApplyResult{Inserted: len(records)}, nil
	}
	return s.result, nil
}

func writeSeedCSV(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "supplier-seed-data.csv")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func stringPointer(value string) *string {
	return &value
}
