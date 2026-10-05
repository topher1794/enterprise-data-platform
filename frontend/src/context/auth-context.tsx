import * as React from "react";

import { authService } from "@/services/authService";
import type { User, Credentials } from "@/types/auth";

const AUTH_DELAY_MS = 700;

const USER_KEY = "edp.auth.user";
const CLUSTER_KEY = "edp.auth.cluster";

export const CLUSTERS = [
  "prod-us-central1.edp.internal",
  "prod-eu-west1.edp.internal",
  "prod-ap-southeast2.edp.internal",
  "stg-us-central1.edp.internal",
  "dev-us-central1.edp.internal",
] as const;

interface AuthContextValue {
  user: User | null;
  cluster: string;
  isLoading: boolean;
  signIn: (credentials: Credentials) => Promise<void>;
  signInWithSso: (provider: string) => Promise<void>;
  unlockWithBiometrics: () => Promise<void>;
  signOut: () => void;
  setCluster: (cluster: string) => void;
}

const AuthContext = React.createContext<AuthContextValue | null>(null);

function readStoredUser(): User | null {
  try {
    return (
      JSON.parse(localStorage.getItem(USER_KEY) ?? "null") as User | null
    );
  } catch {
    return null;
  }
}

function readStoredCluster(): string {
  return localStorage.getItem(CLUSTER_KEY) ?? CLUSTERS[0];
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = React.useState<User | null>(null);
  const [cluster, setClusterState] = React.useState<string>(CLUSTERS[0]);
  const [isLoading, setIsLoading] = React.useState(true);

  React.useEffect(() => {
    setUser(readStoredUser());
    setClusterState(readStoredCluster());
    setIsLoading(false);
  }, []);

  const persist = React.useCallback((nextUser: User) => {
    setUser(nextUser);
    if (nextUser.rememberDevice) {
      localStorage.setItem(USER_KEY, JSON.stringify(nextUser));
    } else {
      localStorage.removeItem(USER_KEY);
    }
  }, []);

  const signIn = React.useCallback(
    async ({ email, password, rememberDevice, requireFido2 }: Credentials) => {
      setIsLoading(true);
      try {
        const result = await authService.login(email, password, rememberDevice);
        persist(result.user);
      } catch (error) {
        const message =
          error instanceof Error
            ? error.message
            : "Invalid email or password.";
        throw new Error(message);
      } finally {
        setIsLoading(false);
      }
    },
    [persist],
  );

  const signInWithSso = React.useCallback(
    async (provider: string) => {
      await new Promise((resolve) => setTimeout(resolve, AUTH_DELAY_MS));
      persist({
        email: `sso.user@${provider.toLowerCase().replace(/\s+/g, "")}.corp`,
        displayName: "SSO User",
        cluster: readStoredCluster(),
        rememberDevice: true,
        requireFido2: false,
        role: "Platform Engineer",
        sessionId: crypto.randomUUID(),
        issuedAt: Date.now(),
      });
    },
    [persist],
  );

  const unlockWithBiometrics = React.useCallback(async () => {
    await new Promise((resolve) => setTimeout(resolve, AUTH_DELAY_MS));
    const stored = readStoredUser();
    persist(
      stored ?? {
        email: "passkey.user@datamesh.corp",
        displayName: "Passkey User",
        cluster: readStoredCluster(),
        rememberDevice: true,
        requireFido2: true,
        role: "Platform Engineer",
        sessionId: crypto.randomUUID(),
        issuedAt: Date.now(),
      },
    );
  }, [persist]);

  const signOut = React.useCallback(() => {
    authService.logout();
    localStorage.removeItem(USER_KEY);
    setUser(null);
  }, []);

  const setCluster = React.useCallback((next: string) => {
    localStorage.setItem(CLUSTER_KEY, next);
    setClusterState(next);
  }, []);

  const value = React.useMemo<AuthContextValue>(
    () => ({
      user,
      cluster,
      isLoading,
      signIn,
      signInWithSso,
      unlockWithBiometrics,
      signOut,
      setCluster,
    }),
    [
      user,
      cluster,
      isLoading,
      signIn,
      signInWithSso,
      unlockWithBiometrics,
      signOut,
      setCluster,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const context = React.useContext(AuthContext);
  if (!context) {
    throw new Error("useAuth must be used within <AuthProvider>");
  }
  return context;
}