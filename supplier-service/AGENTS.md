# Supplier service guidance

This file applies to work in `supplier-service/`. Keep supplier and pickup-location data, its validation, persistence, and history in this service. Coordinate with the user service for administrator identity and permissions, and with the order service for errand references and active-errand checks. Do not move order or credit lifecycle logic into this service.

## Requirements source

- Read [`docs/product-backlog.md`](docs/product-backlog.md) before planning or implementing supplier-service work.
- Treat that document as the source of truth for scope, priorities, planned delivery, functional requirements, applicable non-functional requirements, and cross-service dependencies.
- Keep implementations and tests traceable to the requirement IDs in that document. If this guidance conflicts with it, follow the product backlog and update this file to remove the conflict.

## Supplier records

- Give each supplier or pickup location a system-generated, stable identifier. Never let a client create or change it.
- Store a name, type, building, floor, location description, latitude, longitude, opening hours, and closing hours. An image is optional.
- Validate required fields on the server and identify missing or invalid fields in the response. Require a name and location information; validate coordinates and opening/closing hours before saving.
- Enforce uniqueness of normalized supplier names when creating or renaming records. Apply the same normalization consistently to writes and uniqueness checks.
- Retain previous versions when a record is updated. Keep the supplier identifier stable, give each version a distinct reference, and offer the newest version for new errands.

## Read and management behavior

- Let authenticated users and authorized administrators list available suppliers and pickup locations. Include at least the name and location, and make the remaining record details and optional image available for display.
- Support searching or filtering by name or location. Keep listing usable with up to 1,000 suppliers and pickup locations; use pagination or equivalent bounded responses when lists grow.
- Require server-side administrator authorization for create, update, and delete operations. Derive identity and role from trusted authentication data, never from client-supplied role fields.
- Allow administrators to edit the name, type, building, floor, location description, coordinates, hours, and image. Validate updates by the same rules as creation.
- Reject deletion while an active errand uses the supplier or pickup location. If the active-errand check is unavailable, do not assume deletion is safe. Preserve the data needed to display past errand pickup details after a record is deleted.
- Support administrator-interface confirmation before deletion and unsaved-change warnings when canceling creation or editing; the service must expose clear success and error outcomes for those flows.

## Order-service contract

- Expose the newest available record version for a new errand's pickup-location selection.
- Make the specific version selected at errand creation addressable later. Updates and permitted deletions must not change the pickup details shown for active or past errands.
- Coordinate the deletion guard and version reference with the order service so concurrent errand creation cannot leave an active errand pointing at an unavailable record.

## Data and operations

- Initialize supplier data from `../data/csv/supplier-seed-data.csv`, mapping its `StartingTime`, `ClosingTime`, and optional `ImageURL` columns to the service's record fields. Repeating startup or deployment must not duplicate seed records or overwrite administrator changes.
- Persist records and their versions across normal container restarts. Expose a readiness check that reflects whether required supplier operations can run.
- Keep secrets out of source control, images, logs, and error responses. Use only the service credentials this service needs, and protect service-to-service calls that carry sensitive information.
- Emit structured errors and useful lifecycle logs without authentication tokens or other secrets.

## Verification

Add focused tests for listing and filtering, required-field errors, normalized-name conflicts, authorization, immutable identifiers, version selection and history, deletion with active and past errands, and repeatable seed initialization. Cover the order-service boundary where an errand is created or a deletion is checked. Document one test command that exits nonzero on failure.
