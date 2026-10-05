import type { User } from "@/types/auth";

export interface LoginResponse {
  access_token: string;
  expires_in: number;
  user: User;
}

export interface AuthServiceLoginResult {
  user: User;
  accessToken: string;
  expiresIn: number;
}

export class AuthService {
  private baseUrl: string;

  constructor() {
    // Use relative URL for API; in production this would be the backend host
    // e.g., import.meta.env.VITE_API_BASE_URL or similar
    this.baseUrl = "/api";
  }

  async login(
    email: string,
    password: string,
    rememberDevice = false
  ): Promise<AuthServiceLoginResult> {
    const response = await fetch(`${this.baseUrl}/v1/auth/login`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ email, password }),
    });

    if (!response.ok) {
      const errorData = await response.json().catch(() => ({}));
      throw new Error(
        errorData.detail || "Invalid email or password."
      );
    }

    const data = await response.json() as LoginResponse;

    // Parse user from the response
    const user: User = {
      email: data.user.email,
      displayName: data.user.name || data.user.email.split("@")[0],
      cluster: "prod-us-central1.edp.internal",
      rememberDevice,
      requireFido2: false,
      role: data.user.role || "data_engineer",
      sessionId: crypto.randomUUID(),
      issuedAt: Date.now(),
    };

    return {
      user,
      accessToken: data.access_token,
      expiresIn: data.expires_in,
    };
  }

  async logout(): Promise<void> {
    // In production, would call backend logout to invalidate token
    // For now, just clear client-side state
  }

  async refreshToken(): Promise<AuthServiceLoginResult> {
    // Would refresh the access token using a refresh token
    throw new Error("Not implemented");
  }
}

export const authService = new AuthService();