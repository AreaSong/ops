\set ON_ERROR_STOP on

SELECT
  (
    SELECT count(*)
    FROM pg_tables
    WHERE schemaname = 'public'
      AND tablename <> '_prisma_migrations'
      AND NOT (
        has_table_privilege(:'app_user', format('%I.%I', schemaname, tablename), 'SELECT')
        AND has_table_privilege(:'app_user', format('%I.%I', schemaname, tablename), 'INSERT')
        AND has_table_privilege(:'app_user', format('%I.%I', schemaname, tablename), 'UPDATE')
        AND has_table_privilege(:'app_user', format('%I.%I', schemaname, tablename), 'DELETE')
      )
  )
  || '|'
  || (
    SELECT count(*)
    FROM pg_sequences
    WHERE schemaname = 'public'
      AND NOT (
        has_sequence_privilege(:'app_user', format('%I.%I', schemaname, sequencename), 'USAGE')
        AND has_sequence_privilege(:'app_user', format('%I.%I', schemaname, sequencename), 'SELECT')
        AND has_sequence_privilege(:'app_user', format('%I.%I', schemaname, sequencename), 'UPDATE')
      )
  )
  || '|'
  || CASE
    WHEN has_schema_privilege(:'app_user', 'public', 'CREATE') THEN 'true'
    ELSE 'false'
  END
  || '|'
  || (SELECT count(*) FROM pg_roles WHERE rolname = :'app_user')
  || '|'
  || COALESCE((SELECT rolcanlogin::text FROM pg_roles WHERE rolname = :'app_user'), 'false')
  || '|'
  || COALESCE((SELECT rolsuper::text FROM pg_roles WHERE rolname = :'app_user'), 'false')
  || '|'
  || COALESCE((SELECT rolcreatedb::text FROM pg_roles WHERE rolname = :'app_user'), 'false')
  || '|'
  || COALESCE((SELECT rolcreaterole::text FROM pg_roles WHERE rolname = :'app_user'), 'false')
  || '|'
  || COALESCE((SELECT rolreplication::text FROM pg_roles WHERE rolname = :'app_user'), 'false')
  || '|'
  || COALESCE((SELECT rolbypassrls::text FROM pg_roles WHERE rolname = :'app_user'), 'false')
  || '|'
  || (
    SELECT count(*)
    FROM pg_auth_members memberships
    JOIN pg_roles member_role ON member_role.oid = memberships.member
    WHERE member_role.rolname = :'app_user'
  )
  || '|'
  || CASE
    WHEN to_regclass('public."_prisma_migrations"') IS NULL THEN 'false'
    WHEN has_table_privilege(:'app_user', to_regclass('public."_prisma_migrations"'), 'INSERT')
      OR has_table_privilege(:'app_user', to_regclass('public."_prisma_migrations"'), 'UPDATE')
      OR has_table_privilege(:'app_user', to_regclass('public."_prisma_migrations"'), 'DELETE')
      THEN 'true'
    ELSE 'false'
  END;
