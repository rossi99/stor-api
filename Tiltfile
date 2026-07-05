# Local dev environment: Postgres (docker compose) + migrations + live-reloading API.
# Usage: tilt up

docker_compose('./docker-compose.yml')
dc_resource('db', labels=['infra'])

local_resource(
    'migrate',
    cmd='task migrate',
    resource_deps=['db'],
    deps=['migrations'],
    labels=['infra'],
)

local_resource(
    'sqlc',
    cmd='task sqlc',
    deps=['internal/store/queries', 'sqlc.yaml'],
    labels=['codegen'],
)

local_resource(
    'api',
    serve_cmd='go run ./cmd/api',
    serve_env={
        'DATABASE_URL': 'postgres://stor:stor_dev_password@localhost:5432/stor?sslmode=disable',
        'JWT_SIGNING_KEY': 'dev-only-signing-key-not-for-production!!',
        'PORT': '8080',
        'ENV': 'development',
    },
    deps=['cmd', 'internal', 'go.mod', 'go.sum'],
    resource_deps=['migrate'],
    readiness_probe=probe(http_get=http_get_action(port=8080, path='/healthz'), period_secs=2),
    labels=['app'],
)
