export interface FieldIssue {
  field: string;
  message: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: FieldIssue[];

  constructor(
    status: number,
    code: string,
    message: string,
    fields: FieldIssue[] = [],
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export class NetworkError extends Error {
  constructor(message = "The platform could not be reached.") {
    super(message);
    this.name = "NetworkError";
  }
}

export class TimeoutError extends Error {
  constructor(message = "The request timed out.") {
    super(message);
    this.name = "TimeoutError";
  }
}

export class BackendUnavailableError extends Error {
  constructor(message = "The data source API is not available.") {
    super(message);
    this.name = "BackendUnavailableError";
  }
}

export interface ErrorPresentation {
  title: string;
  description: string;
}

const SENSITIVE_PATTERN = /(password|passwd|pwd|secret|token|api[_-]?key)\s*[:=]\s*\S+/gi;

export function sanitizeMessage(message: string): string {
  return message.replace(SENSITIVE_PATTERN, "$1: [redacted]").trim();
}

export function presentApiError(error: unknown): ErrorPresentation {
  if (error instanceof TimeoutError) {
    return {
      title: "Request timed out",
      description:
        "The platform took too long to respond. Check your connection and try again.",
    };
  }

  if (error instanceof NetworkError) {
    return {
      title: "Connection to the platform failed",
      description:
        "EDP could not reach the API. Check your network connection and try again.",
    };
  }

  if (error instanceof ApiError) {
    const message = sanitizeMessage(error.message);

    switch (error.status) {
      case 400:
        return {
          title: "Request rejected",
          description:
            message || "The request was malformed. Review the form and retry.",
        };
      case 401:
        return {
          title: "Session expired",
          description:
            "Your session is no longer valid. Sign in again to continue.",
        };
      case 403:
        return {
          title: "Permission denied",
          description:
            "Your account does not have permission to perform this action.",
        };
      case 404:
        return {
          title: "Not found",
          description: message || "The requested resource no longer exists.",
        };
      case 409:
        return {
          title: "Duplicate data source",
          description:
            message || "A data source with these details already exists.",
        };
      case 422:
        return {
          title: "Operation failed",
          description:
            message || "The platform could not complete the request.",
        };
      case 429:
        return {
          title: "Too many requests",
          description:
            "You are being rate limited. Wait a moment and try again.",
        };
      case 500:
      case 502:
      case 503:
      case 504:
        return {
          title: "Server error",
          description:
            "Something went wrong on the platform. Try again in a moment.",
        };
      default:
        return {
          title: "Request failed",
          description: message || "The request could not be completed.",
        };
    }
  }

  if (error instanceof Error && error.message) {
    return {
      title: "Request failed",
      description: sanitizeMessage(error.message),
    };
  }

  return {
    title: "Request failed",
    description: "An unexpected error occurred. Try again.",
  };
}
