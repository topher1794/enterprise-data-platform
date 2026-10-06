import { z } from "zod";

import {
  ENVIRONMENTS,
  MVP_SOURCE_TYPES,
  SSL_MODES,
} from "@/features/data-sources/types/dataSource";

const optionalText = (max: number) =>
  z
    .string()
    .trim()
    .max(max, `Must be ${max} characters or fewer.`)
    .optional()
    .or(z.literal(""));

const integerInRange = (min: number, max: number, message: string) =>
  z
    .string()
    .trim()
    .min(1, message)
    .refine(
      (value) =>
        /^\d+$/.test(value) && Number(value) >= min && Number(value) <= max,
      message,
    );

export const basicInformationSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, "Source name is required.")
    .max(200, "Source name must be 200 characters or fewer."),
  description: optionalText(2000),
  type: z.enum(MVP_SOURCE_TYPES, {
    errorMap: () => ({ message: "Select a supported source type." }),
  }),
  environment: z.enum(ENVIRONMENTS, {
    errorMap: () => ({ message: "Select an environment." }),
  }),
  domain: optionalText(100),
  owner: optionalText(120),
  tags: z
    .array(z.string().trim().min(1, "Tags cannot be empty.").max(60))
    .max(10, "A source can carry up to 10 tags."),
});

export type BasicInformationSchemaValues = z.infer<
  typeof basicInformationSchema
>;

export function buildConnectionSchema(databaseLabel: string) {
  return z.object({
    method: z.enum(["direct", "ssh_tunnel"], {
      errorMap: () => ({ message: "Select a connection method." }),
    }),
    host: z
      .string()
      .trim()
      .min(1, "Host is required.")
      .max(255, "Host must be 255 characters or fewer."),
    port: integerInRange(1, 65535, "Enter a valid port between 1 and 65535."),
    database: z
      .string()
      .trim()
      .min(1, `${databaseLabel} is required.`)
      .max(128, `${databaseLabel} must be 128 characters or fewer.`),
    schema: optionalText(128),
    username: z
      .string()
      .trim()
      .min(1, "Username is required.")
      .max(128, "Username must be 128 characters or fewer."),
    password: z
      .string()
      .min(1, "Password is required.")
      .max(512, "Password must be 512 characters or fewer."),
    sslMode: z.enum(SSL_MODES, {
      errorMap: () => ({ message: "Select an SSL mode." }),
    }),
    connectionTimeout: integerInRange(
      1,
      300,
      "Connection timeout must be between 1 and 300 seconds.",
    ),
    queryTimeout: integerInRange(
      1,
      3600,
      "Query timeout must be between 1 and 3600 seconds.",
    ),
    maxConnections: integerInRange(
      1,
      100,
      "Max connections must be between 1 and 100.",
    ),
  });
}

export type ConnectionSchemaValues = z.infer<
  ReturnType<typeof buildConnectionSchema>
>;

export function connectionDatabaseLabel(type: string): string {
  return type === "oracle" ? "Service Name" : "Database Name";
}
