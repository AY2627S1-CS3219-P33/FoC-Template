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
		"/suppliers":                     {"get", "post"},
		"/suppliers/{supplierId}":        {"get", "patch", "delete"},
		"/supplier-versions/{versionId}": {"get"},
		"/readyz":                        {"get"},
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
	}

	parameters := mapping(t, components, "parameters")
	limit := mapping(t, parameters, "Limit")
	limitSchema := mapping(t, limit, "schema")
	require.Equal(t, "25", scalar(t, limitSchema, "default"))
	require.Equal(t, "100", scalar(t, limitSchema, "maximum"))
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

	administration := readYAML(t, "../openapi/administration.yaml")
	deleteOperation := mapping(t, mapping(t, administration, "operations"), "deleteSupplier")
	contract := marshalYAML(t, deleteOperation)
	require.Contains(t, contract, "DeletionOperationID")
	require.Contains(t, contract, "\"202\"")
	require.Contains(t, contract, "DeletionPending")
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
