package tests

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
)

func TestOpenAPIExposesFrozenOperations(t *testing.T) {
	document := readYAML(t, "../openapi.yaml")
	paths := mapping(t, document, "paths")

	expected := map[string][]string{
		"/suppliers":                       {"get", "post"},
		"/suppliers/{supplierId}":          {"get", "patch", "delete"},
		"/supplier-versions/{versionId}":   {"get"},
		"/buildings":                       {"get", "post"},
		"/buildings/{buildingId}":          {"delete"},
		"/supplier-types":                  {"get", "post"},
		"/supplier-types/{supplierTypeId}": {"delete"},
		"/readyz":                          {"get"},
	}
	for path, methods := range expected {
		pathItem := mapping(t, paths, path)
		for _, method := range methods {
			operation := mapping(t, pathItem, method)
			require.NotEmpty(t, scalar(t, operation, "$ref"), "%s %s must reference its owning fragment", method, path)
		}
	}
}

func TestCommonContractContainsBothIdentifiersAndBoundedPagination(t *testing.T) {
	document := readYAML(t, "../openapi/common.yaml")
	components := mapping(t, document, "components")
	schemas := mapping(t, components, "schemas")

	for _, schemaName := range []string{"Supplier", "SupplierVersion"} {
		schema := mapping(t, schemas, schemaName)
		require.Contains(t, marshalYAML(t, schema), "supplierId")
		require.Contains(t, marshalYAML(t, schema), "versionId")
		require.Contains(t, marshalYAML(t, schema), "buildingId")
		require.Contains(t, marshalYAML(t, schema), "typeId")
	}

	parameters := mapping(t, components, "parameters")
	limit := mapping(t, parameters, "Limit")
	limitSchema := mapping(t, limit, "schema")
	require.Equal(t, "25", scalar(t, limitSchema, "default"))
	require.Equal(t, "100", scalar(t, limitSchema, "maximum"))

	sort := mapping(t, parameters, "Sort")
	require.Equal(t, "sort", scalar(t, sort, "name"))
	sortSchema := mapping(t, sort, "schema")
	require.Equal(t, "name", scalar(t, sortSchema, "default"))
	require.Equal(t, "[name -name type -type building -building]", scalar(t, sortSchema, "enum"))

	catalog := readYAML(t, "../openapi/catalog.yaml")
	listSuppliers := mapping(t, mapping(t, catalog, "operations"), "listSuppliers")
	require.Contains(t, marshalYAML(t, listSuppliers), "./common.yaml#/components/parameters/Sort")
}

func TestOpenAPIContainsEverySharedErrorCode(t *testing.T) {
	document := readYAML(t, "../openapi/common.yaml")
	contract := marshalYAML(t, document)
	codes := []apperror.Code{
		apperror.InvalidArgument,
		apperror.Unauthenticated,
		apperror.Forbidden,
		apperror.SupplierNotFound,
		apperror.SupplierVersionNotFound,
		apperror.SupplierNameConflict,
		apperror.SupplierHasActiveErrands,
		apperror.DeletionFenceUnavailable,
		apperror.BuildingNotFound,
		apperror.BuildingConflict,
		apperror.BuildingInUse,
		apperror.SupplierTypeNotFound,
		apperror.SupplierTypeConflict,
		apperror.SupplierTypeInUse,
		apperror.DependencyUnavailable,
		apperror.Internal,
	}
	for _, code := range codes {
		require.Contains(t, contract, string(code))
	}
}

func TestNamedErrorResponsesConstrainTheirStableCodes(t *testing.T) {
	document := readYAML(t, "../openapi/common.yaml")
	responses := mapping(t, mapping(t, document, "components"), "responses")
	expected := map[string]apperror.Code{
		"InvalidArgument":          apperror.InvalidArgument,
		"Unauthenticated":          apperror.Unauthenticated,
		"Forbidden":                apperror.Forbidden,
		"SupplierNotFound":         apperror.SupplierNotFound,
		"SupplierVersionNotFound":  apperror.SupplierVersionNotFound,
		"NameConflict":             apperror.SupplierNameConflict,
		"ActiveErrands":            apperror.SupplierHasActiveErrands,
		"DeletionFenceUnavailable": apperror.DeletionFenceUnavailable,
		"BuildingNotFound":         apperror.BuildingNotFound,
		"BuildingConflict":         apperror.BuildingConflict,
		"BuildingInUse":            apperror.BuildingInUse,
		"SupplierTypeNotFound":     apperror.SupplierTypeNotFound,
		"SupplierTypeConflict":     apperror.SupplierTypeConflict,
		"SupplierTypeInUse":        apperror.SupplierTypeInUse,
		"DependencyUnavailable":    apperror.DependencyUnavailable,
		"Internal":                 apperror.Internal,
	}
	for responseName, code := range expected {
		response := mapping(t, responses, responseName)
		require.Contains(t, marshalYAML(t, response), "const: "+string(code))
	}
}

func TestDeleteContractIsIdempotentAndCanReportPendingReconciliation(t *testing.T) {
	common := readYAML(t, "../openapi/common.yaml")
	parameters := mapping(t, mapping(t, common, "components"), "parameters")
	idempotencyKey := mapping(t, parameters, "DeletionOperationID")
	require.Equal(t, "Idempotency-Key", scalar(t, idempotencyKey, "name"))
	require.Equal(t, "true", scalar(t, idempotencyKey, "required"))
	require.Contains(t, scalar(t, idempotencyKey, "description"), "durably bound")
	require.Contains(t, scalar(t, idempotencyKey, "description"), "another supplier is invalid")

	schemas := mapping(t, mapping(t, common, "components"), "schemas")
	pending := mapping(t, schemas, "DeletionPending")
	require.Contains(t, marshalYAML(t, pending), "const: commit_pending")

	administration := readYAML(t, "../openapi/administration.yaml")
	deleteOperation := mapping(t, mapping(t, administration, "operations"), "deleteSupplier")
	contract := marshalYAML(t, deleteOperation)
	require.Contains(t, contract, "DeletionOperationID")
	require.Contains(t, contract, "\"202\"")
	require.Contains(t, contract, "DeletionPending")
	require.Contains(t, contract, "replay of an already completed")
}

func TestLookupContractMatchesHandlers(t *testing.T) {
	lookups := mapping(t, readYAML(t, "../openapi/lookups.yaml"), "operations")
	for operationID, expected := range map[string]struct {
		permissions string
		statuses    map[string]string
	}{
		"listBuildings":      {"[suppliers:read]", map[string]string{"200": "LookupList", "400": "InvalidArgument", "401": "Unauthenticated", "403": "Forbidden"}},
		"createBuilding":     {"[suppliers:read suppliers:manage]", map[string]string{"201": "Lookup", "400": "InvalidArgument", "403": "Forbidden", "409": "BuildingConflict"}},
		"retireBuilding":     {"[suppliers:read suppliers:manage]", map[string]string{"204": "", "400": "InvalidArgument", "403": "Forbidden", "404": "BuildingNotFound", "409": "BuildingInUse"}},
		"listSupplierTypes":  {"[suppliers:read]", map[string]string{"200": "LookupList", "400": "InvalidArgument", "401": "Unauthenticated", "403": "Forbidden"}},
		"createSupplierType": {"[suppliers:read suppliers:manage]", map[string]string{"201": "Lookup", "400": "InvalidArgument", "403": "Forbidden", "409": "SupplierTypeConflict"}},
		"retireSupplierType": {"[suppliers:read suppliers:manage]", map[string]string{"204": "", "400": "InvalidArgument", "403": "Forbidden", "404": "SupplierTypeNotFound", "409": "SupplierTypeInUse"}},
	} {
		operation := mapping(t, lookups, operationID)
		require.Equal(t, operationID, scalar(t, operation, "operationId"))
		require.Equal(t, expected.permissions, scalar(t, operation, "x-required-permissions"), operationID)
		responses := mapping(t, operation, "responses")
		for status, reference := range expected.statuses {
			response := mapping(t, responses, status)
			require.Contains(t, marshalYAML(t, response), reference, "%s %s", operationID, status)
		}
	}
	require.NotContains(t, marshalYAML(t, lookups), "patch", "lookup names are immutable")

	common := readYAML(t, "../openapi/common.yaml")
	components := mapping(t, common, "components")
	schemas := mapping(t, components, "schemas")
	require.Equal(t, "500", scalar(t, mapping(t, mapping(t, schemas, "LookupList"), "properties")["items"].(map[string]any), "maxItems"))
	require.Equal(t, "[id name retired]", scalar(t, mapping(t, schemas, "Lookup"), "required"))
	for schema, maximum := range map[string]string{"BuildingCreate": "120", "SupplierTypeCreate": "64"} {
		name := mapping(t, mapping(t, mapping(t, schemas, schema), "properties"), "name")
		require.Equal(t, maximum, scalar(t, name, "maxLength"), schema)
	}
	includeRetired := mapping(t, mapping(t, components, "parameters"), "IncludeRetired")
	require.Equal(t, "includeRetired", scalar(t, includeRetired, "name"))
	require.Equal(t, "false", scalar(t, mapping(t, includeRetired, "schema"), "default"))
}

func TestWriteContractMatchesServerStringValidation(t *testing.T) {
	document := readYAML(t, "../openapi/common.yaml")
	schemas := mapping(t, mapping(t, document, "components"), "schemas")
	contract := marshalYAML(t, schemas)
	require.Contains(t, contract, `pattern: \S`)
	require.Contains(t, contract, `pattern: ^https?://`)
}

func readYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, yaml.Unmarshal(content, &document))
	return document
}

func mapping(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, found := parent[key]
	require.True(t, found, "missing key %s", key)
	result, ok := value.(map[string]any)
	require.True(t, ok, "%s is not a mapping", key)
	return result
}

func scalar(t *testing.T, parent map[string]any, key string) string {
	t.Helper()
	value, found := parent[key]
	require.True(t, found, "missing key %s", key)
	return toString(value)
}

func toString(value any) string {
	return fmt.Sprint(value)
}

func marshalYAML(t *testing.T, value any) string {
	t.Helper()
	content, err := yaml.Marshal(value)
	require.NoError(t, err)
	return string(content)
}
