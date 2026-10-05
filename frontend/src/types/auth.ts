export interface User {
  email: string;
  displayName: string;
  cluster: string;
  rememberDevice: boolean;
  requireFido2: boolean;
  role: string;
  sessionId: string;
  issuedAt: number;
}

export interface Credentials {
  email: string;
  password: string;
  rememberDevice: boolean;
  requireFido2: boolean;
}

export type AuthStatus = "authenticated" | "anonymous";
