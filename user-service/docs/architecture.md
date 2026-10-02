# User-service architecture

These diagrams describe the current user-service implementation. Auth0 owns
credentials and sessions; user-service owns local accounts, profiles, roles,
and account state.

## 1. System architecture

```mermaid
flowchart LR
    Browser[User frontend]
    Auth0[Auth0 tenant]
    UserAPI[User service\n:8080]
    UserDB[(User PostgreSQL database)]
    SupplierAPI[Supplier service\n:3002]
    SupplierDB[(Supplier PostgreSQL database)]
    OrderAPI[Order service]
    CreditAPI[Credit service]

    Browser -->|login and tokens| Auth0
    Browser -->|profile and account requests| UserAPI
    Browser -->|supplier requests| SupplierAPI
    UserAPI --> UserDB
    SupplierAPI --> SupplierDB
    OrderAPI -. future service integration .-> SupplierAPI
    OrderAPI -. future service integration .-> CreditAPI
```

Each service owns its own database. Services coordinate through APIs or events,
not by querying another service's database directly.

## 2. Component Level diagram

```mermaid
flowchart TB
    Request["User Requests<br/>Bearer token and request data"]
    Middleware["<b>Middleware</b><br/>────────────────────<br/>Validate JWT<br/>Check permissions"]
    Handler["<b>Handler</b><br/>────────────────────<br/>Route requests<br/>Validate request bodies<br/>Format responses"]
    Service["<b>Service</b><br/>────────────────────<br/>Provision accounts<br/>Check account status<br/>Read and update profiles<br/>Handle account deletion"]
    Auth["<b>Auth</b><br/>────────────────────<br/>Retrieve Auth0 user profiles"]
    Repository["<b>Repository</b><br/>────────────────────<br/>Find and create accounts<br/>Update profiles<br/>Soft-delete accounts"]
    Auth0["Auth0"]
    Database[("User PostgreSQL Database")]
    Response["Results<br/>Profile, confirmation or error"]
    Request --> Middleware
    Auth0 -->|"Signing keys"| Middleware
    Middleware -->|"Authenticated request"| Handler
    Handler --> Service
    Service --> Auth
    Auth0 -->|"Identity profile"| Auth
    Service <-->|"Account operations"| Repository
    Repository <-->|"SQL"| Database
    Service -->|"Result"| Handler
    Handler --> Response
    classDef component fill:#fff,stroke:#2196f3,stroke-width:2px,color:#000;
    class Request,Middleware,Handler,Service,Auth,Repository,Auth0,Database,Response component;
```



## 3. Auth0 login and access-token flow

```mermaid
sequenceDiagram
    actor User
    participant Browser as User frontend
    participant Auth0
    participant API as User or supplier API

    User->>Browser: Enter credentials
    Browser->>Auth0: Authenticate
    Auth0-->>Browser: Access token for requested audience
    Browser->>API: Request with Authorization: Bearer token
    API->>Auth0: Discover/cache JWKS public keys
    API->>API: Verify signature, issuer, audience, expiry
    API-->>Browser: Protected response
```

The API verifies the JWT locally. The token is not decrypted, and the Auth0
private signing key is never shared with the services.

## 4. RBAC authorization flow

```mermaid
flowchart LR
    User[User]
    Role[Auth0 role\nuser or administrator]
    Permissions[API permissions]
    Token[Access token\npermissions claim]
    API[Protected API endpoint]
    AuthN{JWT valid?}
    AuthZ{Required permission present?}

    User --> Role
    Role --> Permissions
    Permissions --> Token
    Token --> API
    API --> AuthN
    AuthN -->|no| Unauthorized[401 Unauthorized]
    AuthN -->|yes| AuthZ
    AuthZ -->|no| Forbidden[403 Forbidden]
    AuthZ -->|yes| Allowed[Run operation]
```

The current administrator role receives the broad user-management permissions.
Local `ADMIN` and `SUPER_ADMIN` accounts have the same API functionality for
now; the local distinction remains available for future policy changes.

## 5. User provisioning sequence

```mermaid
sequenceDiagram
    participant Browser as User frontend
    participant API as User service
    participant Auth0
    participant DB as User database

    Browser->>API: POST /api/auth/provision with access token
    API->>API: Validate JWT and extract sub
    API->>Auth0: GET /userinfo with access token
    Auth0-->>API: Verified subject and profile
    API->>API: Check verified NUS email and profile fields
    API->>DB: Find account by auth0_subject
    alt Account exists
        DB-->>API: Existing account
    else Account does not exist
        API->>DB: Create active USER account
        DB-->>API: New account
    end
    API-->>Browser: Profile with 200 or 201
```

Provisioning never links an existing account by email. The Auth0 subject is the
authoritative external identity key.

## 6. Self-service account flow

```mermaid
sequenceDiagram
    participant Browser as User frontend
    participant API as User service
    participant DB as User database

    Browser->>API: GET/PATCH/DELETE /api/me with access token
    API->>API: Validate JWT and extract Auth0 sub
    API->>API: Check users:*self permission
    API->>DB: Find account by Auth0 subject
    DB-->>API: Authenticated local account
    API->>API: Require active account
    alt GET /api/me
        API-->>Browser: Own profile
    else PATCH /api/me
        Browser->>API: Editable profile fields only
        API->>DB: Update matched local account
        DB-->>API: Updated profile
        API-->>Browser: Updated own profile
    else DELETE /api/me
        Browser->>API: Explicit confirmation
        API->>DB: Soft-delete matched local account
        DB-->>API: Account deactivated
        API-->>Browser: Success
    end
```

The client never supplies the target account ID. The account is selected from
the validated Auth0 `sub`, so `/api/me` can only operate on the caller's own
local account.

## 7. Super Admin Boostrapping

```mermaid
sequenceDiagram
    participant Main as Application Startup
    participant Config
    participant Service
    participant Repository
    participant DB as PostgreSQL
    Main->>Config: Load bootstrap configuration
    Config-->>Main: Auth0 subject, username and email
    Main->>Service: BootstrapSuperAdmin(configuration)
    Service->>Repository: WithBootstrapLock()
    Repository->>DB: Begin transaction
    Repository->>DB: Lock bootstrap coordination row
    Note over Repository,DB: Concurrent instances wait for this lock
    Service->>Repository: Read bootstrap state
    Repository->>DB: Check completion and SUPER_ADMIN existence
    DB-->>Repository: Current state
    Repository-->>Service: Completed? Administrator exists?
    alt Bootstrap completed and administrator exists
        Note over Service: No changes required
    else Bootstrap incomplete and administrator exists
        Service->>Repository: MarkCompleted()
        Repository->>DB: Record completion timestamp
    else Bootstrap incomplete and no administrator exists
        Service->>Service: Validate bootstrap configuration
        Service->>Repository: CreateSuperAdmin()
        Repository->>DB: Insert active SUPER_ADMIN
        Service->>Repository: MarkCompleted()
        Repository->>DB: Record completion timestamp
    else Bootstrap completed but administrator missing
        Service-->>Repository: Recovery required error
    end
    alt Workflow succeeds
        Repository->>DB: Commit transaction
        Repository-->>Service: Success
        Service-->>Main: Bootstrap complete
        Main->>Main: Start HTTP server
    else Validation, database or recovery error
        Repository->>DB: Roll back transaction
        Repository-->>Service: Error
        Service-->>Main: Bootstrap failed
        Main->>Main: Stop startup
    end
```

## 8. Database schema

```mermaid
erDiagram
    accounts {
        uuid id PK
        text auth0_subject UK
        text username UK
        text email UK
        text role
        boolean active
        text display_name
        text mobile_number
        timestamptz created_at
        timestamptz updated_at
        timestamptz deleted_at
    }

    bootstrap_state {
        text name PK
        timestamptz completed_at
    }
```

`accounts.auth0_subject`, `username`, and `email` are unique. Soft-deleted
accounts remain in the database and continue reserving their identities.
`bootstrap_state` is a singleton coordination row used to make initial
super-administrator creation safe across concurrent service starts. It has no
foreign-key relationship to `accounts`.

The migrations under [`../migrations`](../migrations/) are the database source
of truth; this diagram is documentation only.
