# FoC queue service

This folder packages the standalone RabbitMQ broker used by the FoC services.
It owns message transport only; business events and their exchange, queue, and
binding topology are described by the application-level contract in
[`docs/asyncapi.yaml`](../docs/asyncapi.yaml). The contract is a proposal based
on the current event-storming diagram and should be agreed by the service owners
before implementation.

## Start locally

From the repository root:

```sh
docker compose up -d --build queue-service
docker compose ps queue-service
```

RabbitMQ is available to applications in the Compose network at
`amqp://foc:foc-local-dev@queue-service:5672/` with the default local
configuration. The credentials can be overridden with `RABBITMQ_DEFAULT_USER`
and `RABBITMQ_DEFAULT_PASS` in the root `.env` file. These defaults are for
local development only. RabbitMQ reads these values when it initializes its
data directory; changing them later does not change the password in an existing
volume.

The management UI is at <http://localhost:15672>. Sign in with the configured
credentials. AMQP uses port `5672`; the UI uses port `15672`. Both are bound to
the local host and are not exposed on external interfaces by this Compose file.

RabbitMQ data is stored in the `rabbitmq_data` named volume and survives
container recreation. To remove the broker and its stored data, run
`docker compose down -v` from the repository root; this also removes other
Compose-managed volumes.

## Layout

```text
queue-service/
├── Dockerfile       # RabbitMQ broker image and configuration
├── rabbitmq.conf    # Broker configuration
└── README.md        # Local setup and operations
```

This broker image does not provision the application exchanges, queues, or
bindings. The consuming applications should declare their durable queues and
bindings from the shared contract when their messaging code is implemented.
