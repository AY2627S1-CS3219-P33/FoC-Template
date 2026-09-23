# Supplier service

The supplier service will own supplier and pickup-location listing, search,
administration, seed data, and record-versioning behaviour defined in
[docs/product-backlog.md](docs/product-backlog.md).

## API contract

[openapi.yaml](openapi.yaml) is an empty OpenAPI 3.1 template. Add operations to
it as they are implemented. It should describe only behaviour that is currently
available and tested, rather than every planned backlog feature.

The product backlog remains the source of truth for planned scope. Before
starting an endpoint, identify the requirement IDs it satisfies and use them in
the related implementation and tests.

## Incremental workflow

For each API operation:

1. Select one small backlog requirement or closely related group.
2. Add the endpoint and its request, response, security, and error definitions
   to `openapi.yaml`.
3. Validate the OpenAPI document.
4. Write focused tests for the documented behaviour.
5. Implement the endpoint until those tests pass.
6. Confirm that the implementation and contract still agree before committing.

Keep shared definitions under `components` once more than one endpoint needs
them. Examples include supplier schemas, field-level errors, identifier
parameters, authentication schemes, and pagination metadata.

## Suggested implementation order

1. `GET /health/ready`
2. Persistence and idempotent CSV seed initialization
3. `GET /api/v1/suppliers`
4. `GET /api/v1/suppliers/{supplierId}`
5. `POST /api/v1/suppliers`
6. Search and pagination on the supplier list
7. Exact and latest supplier-version reads for the order service
8. `PUT /api/v1/suppliers/{supplierId}`
9. `DELETE /api/v1/suppliers/{supplierId}` with the active-errand guard

Plan the stable supplier ID and immutable version ID in the persistence model
from the beginning, even before update endpoints are implemented. This avoids a
later data-model migration that could break existing errand references.

## Contract validation

From the repository root, run:

```sh
npx --yes @redocly/cli@2.54.1 lint supplier-service/openapi.yaml
```

The command exits nonzero when the contract is invalid. When implementation is
added, combine this check and the automated service tests into one documented
command that exits nonzero if either step fails, as required by NFR6.2.

## Definition of done for an endpoint

An operation is complete when:

- its implemented behaviour is documented in `openapi.yaml`;
- authentication and authorization rules are enforced;
- success, validation, and expected business-rule failures are tested;
- structured responses match the documented schemas; and
- contract validation and service tests pass.
