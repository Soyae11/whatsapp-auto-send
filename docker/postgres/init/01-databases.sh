#!/bin/bash
# One Postgres server, one database per service. Runs once, on first boot of an empty
# pgdata volume — editing it later changes nothing until the volume is destroyed.
#
# Separate owner roles, not one shared superuser: a service can only reach its own tables,
# so "no service reads another's database" is enforced rather than agreed.
set -euo pipefail

create_service_db() {
	local name="$1" password="$2"

	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
		-v name="$name" -v password="$password" <<-'EOSQL'
		CREATE ROLE :"name" WITH LOGIN PASSWORD :'password';
		CREATE DATABASE :"name" OWNER :"name";
	EOSQL

	echo "created database and role: $name"
}

create_service_db baileys "${BAILEYS_DB_PASSWORD:?BAILEYS_DB_PASSWORD is required}"
create_service_db dispatcher "${DISPATCHER_DB_PASSWORD:?DISPATCHER_DB_PASSWORD is required}"
create_service_db console "${CONSOLE_DB_PASSWORD:?CONSOLE_DB_PASSWORD is required}"
