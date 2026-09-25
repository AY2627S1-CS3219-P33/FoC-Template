# Application entry point — planned responsibilities

The entry point loads local environment values when present, validates configuration, connects to PostgreSQL, wires Auth0 JWT validation and account provisioning, serves the embedded test page, runs super-administrator bootstrap before accepting requests, and performs graceful HTTP shutdown. Migrations must be applied separately before startup.

- **F1.6.7, F1.8:** Coordinate configuration loading, database migration execution, and initial super-administrator bootstrap during startup before accepting requests.
- **F1.8.1, F1.8.4, F1.8.7:** Invoke the bootstrap workflow, which must use repository transaction guarantees to initialize at most once across concurrent instances. Do not decide whether to create an account using an unprotected startup check.
- **F1.8.2, F1.8.5–F1.8.6:** Supply deployment configuration to the workflow and stop initialization on required missing or invalid bootstrap values, without logging credentials.

Wire handlers, services, repositories, Auth0 integration, and middleware here. Keep account rules in the service package and credentials in Auth0.

F1.6.7 and F1.8 describe one bootstrap process: startup migration coordination triggers an atomic service workflow linked to an Auth0 identity. Do not create a local password path or embed deployment credentials in migration files.
