# infra

This directory contains two deliberately different Compose entry points:

- `docker-compose.yml` is the existing local-development dependency stack. It
  uses local-only defaults and must not be used for the member-5 server.
- `member5-app.compose.yaml` runs the member-5 API, worker, and one-shot
  migration job. It attaches to the already deployed external network
  `lingow_member5_net`; it does not define, recreate, or publish PostgreSQL or
  Valkey.

## Member-5 application deployment

The server layout keeps the source checkout in `/data/lingow-member5/source`
and this Compose file in `/data/lingow-member5/infra`. Set
`LINGOW_SOURCE_DIR=../source` in the server-only `.env` (the default) and build
the image with:

```sh
docker compose --env-file .env -f member5-app.compose.yaml build
```

For a normal repository checkout, prefix the commands below with
`LINGOW_SOURCE_DIR=..`, or set `LINGOW_API_IMAGE` to an image that has already
been loaded on the server.

The server-only `.env` is read for Compose interpolation and must contain the existing `POSTGRES_*` and
`VALKEY_PASSWORD` values, percent-encoded copies named
`POSTGRES_PASSWORD_URLENCODED` and `VALKEY_PASSWORD_URLENCODED`, plus
`LINGOW_TOKEN_SECRET`, `LINGOW_AUTH_PEPPER`, and `LINGOW_DESTINATION_KEY`.
`LINGOW_AUTH_PEPPER` must be an independent random secret of at least 32 bytes;
do not reuse the JWT signing secret. It does not pass the raw
database passwords into application containers; the Compose file derives
the application connection URLs with the internal endpoints `postgres:5432`
and `valkey:6379`; it never uses the public `15432`/`16379` mappings. For
hex-generated passwords the encoded copies are identical; otherwise generate
the copies with a URL encoder without printing the result. Keep the file at
mode `600` and never commit it.

If the build host cannot reach `proxy.golang.org`, set `LINGOW_GOPROXY` to a
reachable Go module proxy (for example `https://goproxy.cn,direct`) before the
image build. This affects dependency download during build only.

From the directory containing the Compose file and `.env`:

```sh
docker compose --env-file .env -f member5-app.compose.yaml config
docker compose --env-file .env -f member5-app.compose.yaml run --rm migrate
docker compose --env-file .env -f member5-app.compose.yaml up -d api worker
docker compose --env-file .env -f member5-app.compose.yaml ps
```

For an upgrade that changes account migrations, stop the existing application
containers before running the migration job, then start the new API and worker:

```sh
docker compose --env-file .env -f member5-app.compose.yaml stop api worker
docker compose --env-file .env -f member5-app.compose.yaml run --rm migrate
docker compose --env-file .env -f member5-app.compose.yaml up -d api worker
```

New registered accounts store only the peppered `phone_hash_v2`. Existing
legacy accounts retain `phone_hash` only until their next successful phone
verification, when it is replaced with `phone_hash_v2`; migration 8 also clears
the legacy value from accounts that already have v2. Keep the legacy column and
index until every remaining pre-v2 account has completed that one-time upgrade.

`config` expands secrets, so do not paste its output into tickets or logs.
The API is published on `${LINGOW_API_PORT:-10080}` and exposes `/healthz`.
The worker has no public port; its health check only verifies the signal-aware
process is alive. `LINGOW_DELIVERY_PROVIDER=unconfigured` intentionally marks
outbound delivery as failed until a real provider adapter is configured.

Set `LINGOW_DELIVERY_PROVIDER=wecom-bot` to enable enterprise WeChat group-bot
delivery. An authenticated user configures a bot with `PUT
/api/v1/account/wecom-bots/{destination_ref}` and a `webhook_url`; the service
sends a confirmation to the group before encrypting the URL in
`account_destinations`. Later message creation uses only `channel: "wecom_bot"`
and that `destination_ref`. Never place a group-bot webhook in a client, git,
or an environment variable. Enterprise WeChat does not provide a documented
idempotency key, so recovery after an in-flight process crash is recorded as
`delivery_unknown` instead of automatically repeating a group message.

Phone verification remains disabled by default. A local mock receiver can be
enabled only with `LINGOW_APP_ENV=development`, `LINGOW_SMS_PROVIDER=mock-webhook`,
and `LINGOW_SMS_WEBHOOK_URL`; production rejects this setting. The mock receiver
accepts one JSON request shaped as `{"phone":"...","code":"..."}` and must not
write those values into shared logs.
