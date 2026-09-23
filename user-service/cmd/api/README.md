# Application entry point — planned responsibilities

Documentation only; no startup behavior described here is implemented.

- **F1.6.7, F1.8:** Coordinate configuration loading, database migration execution, and initial super-administrator bootstrap during startup before accepting requests.
- **F1.8.1, F1.8.4, F1.8.7:** Invoke the bootstrap workflow, which must use repository transaction guarantees to initialize at most once across concurrent instances. Do not decide whether to create an account using an unprotected startup check.
- **F1.8.2, F1.8.5–F1.8.6:** Supply deployment configuration to the workflow and stop initialization on required missing or invalid bootstrap values, without logging credentials.

Wire handlers, services, repositories, authentication, email, and middleware here in the future. Keep account rules and password handling in their respective packages.

F1.6.7 and F1.8 describe one bootstrap process: startup migration coordination triggers the service workflow; the workflow uses the common password hasher and atomic persistence. Do not create separate SQL and application bootstrap paths or embed deployment credentials in migration files.
