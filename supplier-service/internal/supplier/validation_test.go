package supplier

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validDetails() Details {
	image := "https://example.com/supplier.png"
	return Details{
		Name:                "NUS Co-op",
		Type:                "Shopping",
		Building:            "Central Library",
		Floor:               "1",
		LocationDescription: "Inside the library",
		Latitude:            1.2967866,
		Longitude:           103.7732677,
		OpeningTime:         "09:00",
		ClosingTime:         "18:00",
		ImageURL:            &image,
	}
}

func validCreate() Create {
	details := validDetails()
	return Create{
		Name:                details.Name,
		Type:                details.Type,
		Building:            details.Building,
		Floor:               details.Floor,
		LocationDescription: details.LocationDescription,
		Latitude:            &details.Latitude,
		Longitude:           &details.Longitude,
		OpeningTime:         details.OpeningTime,
		ClosingTime:         details.ClosingTime,
		ImageURL:            details.ImageURL,
	}
}

func TestValidateDetailsAcceptsCompleteSupplier(t *testing.T) {
	require.NoError(t, ValidateDetails(validDetails()))
}

func TestValidateCreateDistinguishesMissingCoordinatesFromZero(t *testing.T) {
	missing := validCreate()
	missing.Latitude = nil
	missing.Longitude = nil

	_, validationErr := ValidateCreate(missing)
	err := &ValidationError{}
	require.ErrorAs(t, validationErr, &err)
	require.ElementsMatch(t, []string{"latitude", "longitude"}, violationFields(err))

	zero := validCreate()
	zeroLatitude := 0.0
	zeroLongitude := 0.0
	zero.Latitude = &zeroLatitude
	zero.Longitude = &zeroLongitude
	details, validationErr := ValidateCreate(zero)
	require.NoError(t, validationErr)
	require.Zero(t, details.Latitude)
	require.Zero(t, details.Longitude)
}

func TestValidateDetailsReportsNamedFields(t *testing.T) {
	details := validDetails()
	details.Name = " "
	details.Latitude = math.NaN()
	details.OpeningTime = ""
	details.ImageURL = pointer("file:///tmp/image.png")

	err := &ValidationError{}
	require.ErrorAs(t, ValidateDetails(details), &err)
	require.ElementsMatch(t,
		[]string{"name", "latitude", "openingTime", "imageUrl"},
		violationFields(err),
	)
}

func TestNormalizeNameCollapsesWhitespaceAndCase(t *testing.T) {
	require.Equal(t, "nus co-op", NormalizeName("  NUS\t Co-op  "))
}

func TestPatchCreatesProspectiveDetailsWithoutChangingIdentity(t *testing.T) {
	existing := validDetails()
	newName := "The NUS Co-op"
	patch := Patch{Name: &newName, ImageURLSet: true, ImageURL: nil}

	updated := patch.Apply(existing)

	require.Equal(t, "The NUS Co-op", updated.Name)
	require.Nil(t, updated.ImageURL)
	require.Equal(t, "NUS Co-op", existing.Name)
	require.NotNil(t, existing.ImageURL)
	require.False(t, patch.Empty())
	require.True(t, (Patch{}).Empty())
}

func TestValidateListFilterAppliesPageBounds(t *testing.T) {
	require.NoError(t, ValidateListFilter(ListFilter{}))
	require.Equal(t, DefaultPageSize, (ListFilter{}).PageSize())
	require.NoError(t, ValidateListFilter(ListFilter{Limit: MaximumPageSize}))
	require.Error(t, ValidateListFilter(ListFilter{Limit: MaximumPageSize + 1}))
	require.Error(t, ValidateListFilter(ListFilter{Limit: -1}))
	require.Error(t, ValidateListFilter(ListFilter{Query: strings.Repeat("x", 121)}))
	for _, sort := range append([]ListSort{""}, ListSorts...) {
		require.NoError(t, ValidateListFilter(ListFilter{Sort: sort}))
	}
	require.Error(t, ValidateListFilter(ListFilter{Sort: "created"}))
	require.Equal(t, SortName, (ListFilter{}).SortOrder())
}

func TestIdentifierValidationUsesUUIDs(t *testing.T) {
	require.True(t, ValidSupplierID("8c886f56-cb4e-4f95-a6cb-f0baca70f6c6"))
	require.True(t, ValidVersionID("019925d7-9248-7a73-9a40-9d60f37dbd2d"))
	require.False(t, ValidSupplierID("client-chosen-id"))
	require.False(t, ValidVersionID(""))
	require.True(t, ValidDeletionOperationID("019925d7-9248-7a73-9a40-9d60f37dbd2d"))
	require.False(t, ValidDeletionOperationID("retry-1"))
}

func violationFields(err *ValidationError) []string {
	fields := make([]string, 0, len(err.Violations))
	for _, violation := range err.Violations {
		fields = append(fields, violation.Field)
	}
	return fields
}

func pointer(value string) *string { return &value }

// lookupKeyCases include non-ASCII letters, which LookupKey keeps, and
// non-ASCII digits, which it drops like PostgreSQL's pg_c_utf8 [:alnum:].
var lookupKeyCases = map[string]string{
	"Com 2":                     "com2",
	"Com2":                      "com2",
	"  COM-2 ":                  "com2",
	"Prince George's Park":      "princegeorgespark",
	"Prince George\u2019s Park": "princegeorgespark",
	"Food/Coffee":               "foodcoffee",
	"innovation4.0":             "innovation40",
	"Medicine+Science Library":  "medicinesciencelibrary",
	"Café Été":                  "caféété",
	"ÉCOLE":                     "école",
	"中央图书馆 1":                   "中央图书馆1",
	"Ⅷ Hall":                    "ⅷhall",
	"हिंदी":                     "हिंदी",
	"٣ Block":                   "block",
	"---":                       "",
}

func TestLookupKeyKeepsUnicodeLettersAndDigitsOnly(t *testing.T) {
	for input, expected := range lookupKeyCases {
		require.Equal(t, expected, LookupKey(input), "LookupKey(%q)", input)
	}
}

func TestValidateDetailsRequiresALookupKeyForBuildingAndType(t *testing.T) {
	details := validDetails()
	details.Building = "---"
	details.Type = " / "

	err := &ValidationError{}
	require.ErrorAs(t, ValidateDetails(details), &err)
	require.ElementsMatch(t, []FieldViolation{
		{Field: "type", Message: "must contain a letter or digit"},
		{Field: "building", Message: "must contain a letter or digit"},
	}, err.Violations)

	details.Building, details.Type = "", ""
	require.ErrorAs(t, ValidateDetails(details), &err)
	require.ElementsMatch(t, []FieldViolation{
		{Field: "type", Message: "is required"},
		{Field: "building", Message: "is required"},
	}, err.Violations)
}

func TestPatchClearsTheIDOfAPatchedLookupOnly(t *testing.T) {
	existing := validDetails()
	existing.BuildingID, existing.TypeID = "building-id", "type-id"
	building := "Com2"

	updated := Patch{Building: &building}.Apply(existing)

	require.Equal(t, "Com2", updated.Building)
	require.Empty(t, updated.BuildingID)
	require.Equal(t, "type-id", updated.TypeID)
}
