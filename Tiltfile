docker_compose('./docker-compose.yml')
dc_resource('db', labels=['infra'])

local_resource(
    'sqlc',
    cmd='sqlc generate',
    deps=['internal/store/queries.sql', 'internal/store/migrations', 'sqlc.yaml'],
    labels=['codegen'],
)

local_resource(
    'api',
    serve_cmd='go run ./cmd/stor',
    serve_env={
        'DATABASE_URL': 'postgres://stor:stor_dev_password@localhost:5433/stor?sslmode=disable',
        'PORT': '8080',
    },
    deps=['cmd', 'internal', 'go.mod', 'go.sum'],
    resource_deps=['db', 'sqlc'],
    readiness_probe=probe(http_get=http_get_action(port=8080, path='/healthz'), period_secs=2),
    labels=['app'],
)
