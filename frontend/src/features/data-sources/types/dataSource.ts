export const DATA_SOURCE_TYPES = [
  "postgresql",
  "mysql",
  "sqlserver",
  "oracle",
  "rest_api",
  "s3",
  "gcs",
  "azure_data_lake",
  "csv",
  "excel",
] as const;

export type DataSourceType = (typeof DATA_SOURCE_TYPES)[number];

export const MVP_SOURCE_TYPES = [
  "postgresql",
  "mysql",
  "sqlserver",
  "oracle",
] as const;

export type MvpDataSourceType = (typeof MVP_SOURCE_TYPES)[number];

export const SOURCE_TYPE_LABELS: Record<DataSourceType, string> = {
  postgresql: "PostgreSQL",
  mysql: "MySQL",
  sqlserver: "SQL Server",
  oracle: "Oracle",
  rest_api: "REST API",
  s3: "Amazon S3",
  gcs: "Google Cloud Storage",
  azure_data_lake: "Azure Data Lake",
  csv: "CSV",
  excel: "Excel",
};

export const SOURCE_TYPE_ICONS: Record<DataSourceType, string> = {
  postgresql: "database",
  mysql: "database",
  sqlserver: "storage",
  oracle: "dns",
  rest_api: "language",
  s3: "cloud",
  gcs: "cloud",
  azure_data_lake: "cloud",
  csv: "description",
  excel: "table_view",
};

export const ENVIRONMENTS = [
  "development",
  "qa",
  "staging",
  "production",
] as const;

export type Environment = (typeof ENVIRONMENTS)[number];

export const ENVIRONMENT_LABELS: Record<Environment, string> = {
  development: "Development",
  qa: "QA",
  staging: "Staging",
  production: "Production",
};

export const DOMAINS = [
  "sales",
  "finance",
  "hr",
  "operations",
  "customer",
  "supply_chain",
  "other",
] as const;

export const DOMAIN_LABELS: Record<(typeof DOMAINS)[number], string> = {
  sales: "Sales",
  finance: "Finance",
  hr: "HR",
  operations: "Operations",
  customer: "Customer",
  supply_chain: "Supply Chain",
  other: "Other",
};

export type ConnectionMethod = "direct" | "ssh_tunnel";

export const SSL_MODES = [
  "require",
  "verify-ca",
  "verify-full",
  "disable",
] as const;

export type SslMode = (typeof SSL_MODES)[number];

export const SSL_MODE_LABELS: Record<SslMode, string> = {
  require: "Require",
  "verify-ca": "Verify CA",
  "verify-full": "Verify Full",
  disable: "Disable",
};

export type DataSourceStatus =
  | "connected"
  | "warning"
  | "failed"
  | "pending"
  | "disabled";

export const STATUS_LABELS: Record<DataSourceStatus, string> = {
  connected: "Connected",
  warning: "Warning",
  failed: "Failed",
  pending: "Pending",
  disabled: "Disabled",
};

export interface BasicInformationValues {
  name: string;
  description: string;
  type: DataSourceType;
  environment: Environment;
  domain: string;
  owner: string;
  tags: string[];
}

export interface ConnectionFormValues {
  method: ConnectionMethod;
  host: string;
  port: string;
  database: string;
  schema: string;
  username: string;
  password: string;
  sslMode: SslMode;
  connectionTimeout: string;
  queryTimeout: string;
  maxConnections: string;
}

export interface ConnectionPayload {
  method: ConnectionMethod;
  host: string;
  port: number;
  database: string;
  schema: string;
  username: string;
  password: string;
  ssl_mode: SslMode;
  connection_timeout: number;
  query_timeout: number;
  max_connections: number;
}

export interface CreateDataSourceRequest {
  name: string;
  description: string;
  type: DataSourceType;
  environment: Environment;
  domain: string;
  owner_id: string;
  tags: string[];
  connection: ConnectionPayload;
}

export interface DataSourceSummary {
  id: string;
  name: string;
  description: string;
  type: DataSourceType;
  environment: Environment;
  domain: string;
  owner_id: string;
  owner_name: string;
  tags: string[];
  status: DataSourceStatus;
  dataset_count: number;
  row_count: number;
  last_sync_at: string | null;
  created_at: string;
  secret_ref: string;
  connection: {
    method: ConnectionMethod;
    host: string;
    port: number;
    database: string;
    schema: string;
    username: string;
    ssl_mode: SslMode;
  };
}

export interface ListDataSourcesResponse {
  items: DataSourceSummary[];
  total: number;
}

export interface UpdateDataSourceRequest {
  name?: string;
  description?: string;
  environment?: Environment;
  tags?: string[];
}

export interface ConnectionChecks {
  network: boolean;
  authentication: boolean;
  database_access: boolean;
  schema_access: boolean;
}

export interface TestConnectionResult {
  success: boolean;
  message: string;
  response_time_ms: number;
  checks: ConnectionChecks;
}

export interface TestConnectionRequest {
  type: DataSourceType;
  connection: ConnectionPayload;
}

export interface DiscoveredColumn {
  name: string;
  data_type: string;
  nullable: boolean;
  primary_key: boolean;
  description: string;
}

export interface DiscoveredIndex {
  name: string;
  columns: string[];
  unique: boolean;
}

export interface DiscoveredRelationship {
  from_table: string;
  from_column: string;
  to_table: string;
  to_column: string;
}

export interface DiscoveredTable {
  name: string;
  description: string;
  row_count: number | null;
  columns: DiscoveredColumn[];
  indexes: DiscoveredIndex[];
  relationships: DiscoveredRelationship[];
  sample_data: Record<string, string | number | boolean | null>[];
}

export interface DiscoveredSchemaNode {
  name: string;
  tables: DiscoveredTable[];
}

export interface DiscoveredSchemaModel {
  schemas: DiscoveredSchemaNode[];
}

export interface SchemaSummary {
  schema_count: number;
  table_count: number;
  column_count: number;
}

export interface SelectedDataset {
  key: string;
  schema: string;
  table: string;
  row_count: number | null;
  description: string;
}

export interface RegisterDatasetsRequest {
  datasets: { schema: string; table: string }[];
}

export interface TargetConfiguration {
  layer: "Bronze";
  format: "Parquet";
  base_path: string;
}

export const DEFAULT_TARGET_CONFIGURATION: TargetConfiguration = {
  layer: "Bronze",
  format: "Parquet",
  base_path: "gs://edp-data/bronze/postgres",
};

export function datasetKey(schema: string, table: string): string {
  return `${schema}.${table}`;
}

export function summarizeDatasets(datasets: SelectedDataset[]) {
  const schemas = new Set(datasets.map((dataset) => dataset.schema));
  const rows = datasets.reduce(
    (total, dataset) => total + (dataset.row_count ?? 0),
    0,
  );
  return {
    total: datasets.length,
    rows,
    schemas: schemas.size,
  };
}

export function summarizeSchema(model: DiscoveredSchemaModel): SchemaSummary {
  let tableCount = 0;
  let columnCount = 0;
  for (const schema of model.schemas) {
    tableCount += schema.tables.length;
    for (const table of schema.tables) {
      columnCount += table.columns.length;
    }
  }
  return {
    schema_count: model.schemas.length,
    table_count: tableCount,
    column_count: columnCount,
  };
}
