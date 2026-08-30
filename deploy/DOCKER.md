# Sub2API Docker Image

Sub2API is an AI API Gateway Platform for distributing and managing AI product subscription API quotas.

## Quick Start

```bash
docker run -d \
  --name sub2api \
  -p 8080:8080 \
  -e DATABASE_URL="postgres://user:pass@host:5432/sub2api" \
  -e REDIS_URL="redis://host:6379" \
  ghcr.io/jhupo/sub2api:latest
```

## Docker Compose

```yaml
version: '3.8'

services:
  sub2api:
    image: ghcr.io/jhupo/sub2api:latest
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://postgres:postgres@db:5432/sub2api?sslmode=disable
      - REDIS_URL=redis://redis:6379
    depends_on:
      - db
      - redis

  db:
    image: postgres:15-alpine
    environment:
      - POSTGRES_USER=postgres
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=sub2api
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data

volumes:
  postgres_data:
  redis_data:
```

## Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string | Yes | - |
| `REDIS_URL` | Redis connection string | Yes | - |
| `PORT` | Server port | No | `8080` |
| `GIN_MODE` | Gin framework mode (`debug`/`release`) | No | `release` |

## Supported Architectures

- `linux/amd64`
- `linux/arm64`

## Tags

- `latest` - Latest stable release
- `x.y.z` - Specific version
- `x.y` - Latest patch of minor version
- `x` - Latest minor of major version

## One-click rolling updates

The admin version menu uses the regular binary updater by default. A Docker
container cannot safely replace the executable inside its image, so production
Compose deployments should opt into the orchestrated updater instead.

The image contains `update-orchestrator.sh` at
`/usr/local/bin/sub2api-update`. Configure the application container with:

```yaml
environment:
  UPDATE_STRATEGY: orchestrated
  UPDATE_ORCHESTRATOR: /usr/local/bin/sub2api-update
  SUB2API_UPDATE_MODE: image
  SUB2API_UPDATE_COMPOSE_FILE: /opt/sub2api/docker-compose.yml
  SUB2API_UPDATE_SERVICES: sub2api-1,sub2api-2,sub2api-3
  SUB2API_UPDATE_HEALTH_URLS: http://127.0.0.1:7101/readyz,http://127.0.0.1:7102/readyz,http://127.0.0.1:7103/readyz
  SUB2API_UPDATE_PROJECT: sub2api
  SERVER_SHUTDOWN_TIMEOUT_SECONDS: "600"
  # Keep Docker's hard stop above the application drain window.
  SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS: "610"
  # Optional command wrapper when the container cannot use the socket group.
  # Example: sudo -n docker (requires a narrowly-scoped host sudo rule).
  SUB2API_UPDATE_DOCKER_COMMAND: docker
  SUB2API_UPDATE_AUTO_DOCKER_GROUP: "true"
  # Optional when .env is mode 600 and the app runs as a non-root user.
  SUB2API_UPDATE_HELPER_IMAGE: ghcr.io/jhupo/sub2api:latest
stop_grace_period: 610s
volumes:
  - /var/run/docker.sock:/var/run/docker.sock
  - /opt/sub2api:/opt/sub2api:ro
# Match the host socket group (get it with: stat -c '%g' /var/run/docker.sock).
group_add:
  - "989"
```

The image automatically mirrors the mounted socket's numeric group into the
`sub2api` user's supplementary groups before dropping privileges. This keeps
the documented `group_add` form optional for standard Compose deployments; set
`SUB2API_UPDATE_AUTO_DOCKER_GROUP=false` when group membership must remain
explicit. The socket is still a deliberate host-control capability and should
only be mounted for a trusted deployment.

The application image includes the Docker CLI, but Docker socket access is
intentionally opt-in because it grants host-level control. The Compose project
must be mounted at the same path inside the updater container. In `image` mode
the script pulls the target image, recreates each configured service in order,
waits for its health endpoint, and restores the previous version if any step
fails. If the mounted `.env` is root-only, the script starts the configured
helper image as UID 0 and inherits the current container's mounts, including
the Compose file, env file, Docker socket, and data volume; it never changes
secret-file permissions. Compose files should use the version variable so the
target image is unambiguous:

```yaml
image: ghcr.io/jhupo/sub2api:${SUB2API_VERSION:-latest}
```

The API atomically records `pending`, starts the orchestrator in the background,
and returns the operation ID before image pulls or rolling work begin. When the
updater reaches the service that hosts it, it starts a detached helper from the
known-good current image. The helper inherits the current container's mounts,
including the read-only Compose project and Docker socket, recreates the final
service at the target version, waits for readiness, and recreates the previous
version if readiness fails. The orchestrator records `succeeded`, `rolled_back`,
or `failed` under `/app/data/update-status`; the admin UI follows that shared
status before reporting completion. A bounded heartbeat renews the pending
lease while the orchestrator or helper is alive, so slow pulls cannot release
the cross-request update lock. These files do not change the PostgreSQL database
or Redis cache.

During each replacement the updater sends `SIGTERM` to exactly one replica.
The Go server closes its listener immediately, which prevents new requests
from entering that replica, and then waits for active HTTP/SSE requests to
finish. The updater passes `SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS` to every
`docker compose up --timeout` call as the hard stop; set the same value as the
service's `stop_grace_period`, and keep it greater than
`SERVER_SHUTDOWN_TIMEOUT_SECONDS`. Only after the replica exits, starts on the
new version, and passes its health check does the updater continue to the next
service.

When the release image is private or the deployment uses a locally built image,
use `runtime` mode instead. It downloads and verifies the matching release
binary, atomically replaces a mounted runtime path, and then performs the same
rolling health checks and rollback. For Docker, the path must be the path inside
the updater container and services are found by Compose labels, so no Compose
file or `.env` read is needed during the rollout:

```yaml
environment:
  UPDATE_STRATEGY: orchestrated
  UPDATE_ORCHESTRATOR: /usr/local/bin/sub2api-update
  SUB2API_UPDATE_MODE: runtime
  SUB2API_UPDATE_RUNTIME_PATH: /app/runtime/sub2api
  SUB2API_UPDATE_REPOSITORY: jhupo/sub2api
  SUB2API_UPDATE_PROJECT: sub2api
  SUB2API_UPDATE_SERVICES: api,worker
command: ["/app/runtime/sub2api"]
volumes:
  - /opt/sub2api/runtime:/app/runtime
  - /var/run/docker.sock:/var/run/docker.sock
group_add:
  - "989"
```

For the shipped systemd service, keep the default `UPDATE_STRATEGY=binary`.
After atomically replacing the verified binary, the admin UI asks the running
process to exit and systemd's `Restart=always` starts the new version. An
API-triggered orchestrated runtime update with
`SUB2API_UPDATE_RESTART_COMMAND` is rejected: a child finalizer remains in the
service cgroup and cannot survive `systemctl restart` to verify readiness. The
restart-command backend remains available only when an operator invokes the
orchestrator manually from an external shell.

The updater API does not expose arbitrary shell commands; it executes only the
absolute path configured in `UPDATE_ORCHESTRATOR` and passes the current and
target versions as arguments.

## Links

- [GitHub Repository](https://github.com/jhupo/sub2api)
- [Documentation](https://github.com/jhupo/sub2api#readme)
