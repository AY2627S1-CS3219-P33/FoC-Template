## Scope

Own supplier and pickup-location listing, search, creation, update, deletion, seed data, and record-versioning behaviour defined by F2.

## Owned functional requirements

### F2 Support CRUD operations of suppliers or pickup locations (M3)

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| F2.1 | The system shall display a list of the available suppliers and pickup locations for both authenticated users and authorised administrators, showing minimally their name and location | High | 1 |
| F2.1.1 | The system shall show the type (e.g. food, shopping), building, floor, location description, latitude, longitude, opening hours, and closing hours along with an optional image | High | 1 |
| F2.1.2 | The system shall allow suppliers or pickup location to be searched or filtered by name or location | Low | 2 |
| F2.1.3 | The list of suppliers and pickup locations shall be seeded with the initial data from the template repository and additional records added by the admins | High | 1 |
| F2.2 | The system shall allow an authorised administrator to create supplier or pickup location records | High | 1 |
| F2.2.1 | The system shall require the administrator to provide the mandatory fields of supplier name, type (e.g. food, shopping), building, floor, location description, latitude, longitude, opening hours, and closing hours along with an optional image | High | 1 |
| F2.2.2 | The system shall reject incomplete information of supplier or pickup location and enforce location and name of the record to be required. The admin shall be informed about which information is missing. | Medium | 2 |
| F2.2.3 | The system shall ensure that the normalised name of the supplier or pickup location record created is unique | High | 1 |
| F2.2.4 | The system shall give the administrator an option to cancel creating the record. When cancelling, the admin shall be warned that all progress will be lost if the administrator wishes to continue | Low | 4 |
| F2.3 | The system shall allow an authorised administrator to update existing supplier or pickup location records | Medium | 3 |
| F2.3.1 | The system shall permit modifications to the following fields: supplier name, type, building, floor, location description, latitude, longitude, opening hours, closing hours, and image | High | 3 |
| F2.3.2 | The system shall strictly prevent the modification of the system-generated unique identifier associated with the supplier or pickup location record | High | 3 |
| F2.3.3 | The system shall give the administrator an option to cancel updating the record. When cancelling, the admin shall be warned that all progress will be lost if the administrator wishes to continue | Low | 4 |
| F2.4 | The system shall allow an authorised administrator to delete supplier or pickup location records | High | 1 |
| F2.4.1 | The system shall get confirmation from the administrator that they actually want to delete the record | Low | 4 |
| F2.4.2 | The system shall not allow a deletion of a record that is currently being used in an active errand | High | 1 |
| F2.4.3 | Past errand pickup details shall remain readable even after the supplier or pickup location of that errand is deleted | Medium | 3 |
| F2.5 | The system shall implement record versioning for supplier and pickup location updates, retaining previous versions of the record | Medium | 2 |
| F2.5.1 | The system shall ensure that an active or past errand remains linked to the specific version of the supplier or pickup location record that was active at the time of the errand’s creation | High | 2 |
| F2.5.2 | The system shall display the newest version of the supplier or pickup location for all new errand requests | High | 2 |

## Applicable non-functional requirements

These are the shared NFRs most directly applicable to this area. Consult `../shared/non-functional-requirements.md` for the complete NFR set.

### NFR1 Performance - Capacity

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| NFR1.2 | The system shall support all the suppliers and locations within NUS | Medium | 2 |
| NFR1.2.1 | The system shall support of up to 1000 suppliers and locations | Medium | 2 |

### NFR2 Usability and Responsive UI design

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| NFR2.1 | All pages shall display their primary content and enable their main controls within two seconds of navigation being initiated under the documented test environment | Medium | 2 |
| NFR2.2 | The interface shall provide clear feedback for successful, invalid and unsuccessful actions | High | 1 |
| NFR2.2.1 | A rejected form submission (e.g. failed errand request) should identify which fields are invalid, preserve the other valid inputs and display feedback within one second after the response is received | High | 1 |
| NFR2.3 | All mandatory requester, courier, and administrator workflows should be usable at viewport sizes from 360 x 800 pixels to 1440 x 900 pixels without clipped controls or page-level horizontal scrolling | High | 2 |
| NFR2.4 | All mandatory user workflows should work on the latest stable version of Chrome, Safari, Firefox, and Edge | High | 2 |
| NFR2.4.1 | Users should be able to complete each mandatory workflow on every supported browser without browser-specific errors that prevent completion | High | 2 |
| NFR2.4.2 | On every supported browser, text should remain readable and required controls should remain visible and operable, without overlapping content or clipped controls at the supported viewport sizes specified in NFR2.3 | Medium | 3 |
| NFR2.5 | The system must implement efficient data loading techniques such as lazy-loading or pagination on long lists to ensure seamless user interaction | Low | 5 |

### NFR3 Security and Privacy

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| NFR3.3 | The system shall enforce server-side authorisation for every protected operation | High | 1 |
| NFR3.3.1 | The system shall determine the authenticated account’s identity and role using server-controlled session or token information and shall not trust identity, role, or privilege information supplied separately by the client | High | 1 |
| NFR3.3.2 | The system shall determine resource-specific permissions using authoritative server-side data, including errand ownership, courier assignment, account role, and account status | High | 1 |
| NFR3.3.3 | The system shall deny a protected operation when the requester is unauthenticated or lacks an explicitly granted permission | High | 1 |
| NFR3.4 | The system shall protect application secrets and deployment-specific configuration | High | 1 |
| NFR3.4.1 | The system shall not commit passwords, private keys, API credentials, initial super-administrator credentials, or other secrets to source control | High | 1 |
| NFR3.4.2 | The system shall not embed secrets in application source code or container images | High | 1 |
| NFR3.4.3 | The system shall inject deployment secrets at runtime through the deployment environment or an approved secret-management mechanism | High | 1 |
| NFR3.4.4 | The system shall restrict each service to the secrets required for that service’s responsibilities | High | 1 |
| NFR3.5 | The system shall protect communication between backend services against interception and service impersonation | High | 3 |
| NFR3.5.1 | Backend services shall exchange credentials, personal data, and other sensitive information through authenticated connections encrypted using TLS 1.2 or later | Medium | 3 |
| NFR3.5.2 | Each backend service shall verify the identity of the destination service before transmitting sensitive information | High | 3 |
| NFR3.5.3 | Service credentials shall grant only the permissions required by the corresponding service | High | 3 |

### NFR4 Deployability and Portability

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| NFR4.1 | All application services and supporting components should be containerised for reproducible local deployment | High | 2 |
| NFR4.1.1 | From a documented fresh environment with Docker installed, one documented command should start all required services, databases and messaging components | Medium | 2 |
| NFR4.1.2 | Each application service shall expose a readiness check indicating whether it can perform its required operations. | Medium | 2 |
| NFR4.1.3 | Persisted accounts, suppliers, errands, and credit records should survive a normal container restart | High | 2 |
| NFR4.1.4 | The deployment procedure shall initialise the required database schemas and supplier seed data. Repeating the procedure shall not duplicate seed records or overwrite existing application data | High | 2 |
| NFR4.1.5 | Deployment documentation shall specify supported host environments, required software versions, resource requirements, configuration values, startup commands, and procedures for verifying readiness | Medium | 2 |

### NFR5 Performance - Responsiveness

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| NFR5.1 | Under the documented reference workload and NFR1 reference dataset, at least 95% of requests for each core operation shall complete within two seconds, measured from receipt at the application’s public API boundary until the response is sent | Medium | 3 |
| NFR5.1.1 | Core operations shall include login, supplier listing, open-errand listing, errand creation, acceptance, pickup, delivery marking, requester confirmation, cancellation, and credit-balance retrieval | Medium | 3 |
| NFR5.1.2 | The reference workload shall simulate 100 concurrently active users and specify the operation mix, request frequency or think time, test duration, and reference-environment resources | Medium | 3 |
| NFR5.1.3 | Unexpected server errors and timeouts shall affect no more than 1% of valid requests during the measurement period. Expected business-rule rejections shall be reported separately | Medium | 3 |

### NFR6 Testability and observability

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| NFR6.2 | There should be a documented automated test command that runs the mandatory tests and reports a non-zero exit status when any test fails | High | 2 |
| NFR6.3 | Each service should emit structured error and lifecycle logs, while excluding credentials and other secrets | Medium | 2 |
| NFR6.4 | The system shall provide centralised logs, metrics and request traces for communication between backend services | Medium | 3 |

### NFR7 Service Reliability

| ID | Requirement | Priority | Planned sprint |
| --- | --- | --- | --- |
| NFR7.1 | The system shall apply configurable timeouts, retries and circuit breaking mechanisms to prevent the failures in one service to cascade to other services | Medium | 3 |
| NFR7.3 | Repeated processing of the same logical operation shall not create duplicate errands, notifications, initial credit allocations, reservations, releases, or transfers | High | 2 |
| NFR7.3.1 | Automated retries shall be bounded by documented attempt or elapsed-time limits and shall apply only to failures and operations for which retrying is safe | Medium | 2 |
| NFR7.4 | Once an operation requiring asynchronous processing has been acknowledged as accepted, the system shall retain sufficient durable information to resume or reconcile it after an application-service or messaging-component restart | Medium | 3 |
| NFR7.4.1 | An asynchronous operation that exhausts its retry policy shall remain identifiable with its processing status and failure reason and shall not be silently discarded | Medium | 3 |
| NFR7.4.2 | Where manual replay is supported, replaying a failed operation shall preserve the duplicate-effect protections in NFR7.3 | Medium | 3 |

## Cross-service requirements

- F2.2-F2.4 require administrator authorisation supplied by the user/account domain.
- F2.4.2 requires preventing deletion when a supplier or pickup location is used by an active errand; this involves order state.
- F2.4.3 and F2.5 require past and active errands to retain readable, version-specific pickup details for the order domain.
- F3.1.1 requires order creation to use an available pickup location owned by this service.
